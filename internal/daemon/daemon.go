// Package daemon provides PID-file based process lifecycle management
// for the clawdex gateway service.
package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Rememorio/clawdex/internal/secret"
	"github.com/Rememorio/clawdex/internal/termcolor"
)

var term = termcolor.New(os.Stdout)
var currentExecutable = os.Executable

func dim(s string) string {
	return term.Dim(s)
}

func green(s string) string {
	return term.Green(s)
}

func red(s string) string {
	return term.Red(s)
}

func cyan(s string) string {
	return term.Cyan(s)
}

const (
	pidFileName      = "gateway.pid"
	logFileName      = "gateway.log"
	workspaceDirName = "workspace"
	stopGraceTime    = 5 * time.Second
	// startProbeDelay is how long Start waits after fork to verify
	// the child process hasn't crashed (e.g. due to port conflict).
	startProbeDelay = 1500 * time.Millisecond
)

// DataDir returns ~/.clawdex, creating it if needed.
func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".clawdex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create data directory: %w", err)
	}
	return dir, nil
}

// WorkspaceDir returns ~/.clawdex/workspace, creating it if needed.
func WorkspaceDir() (string, error) {
	dataDir, err := DataDir()
	if err != nil {
		return "", err
	}
	workspaceDir := filepath.Join(dataDir, workspaceDirName)
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return "", fmt.Errorf("create workspace directory: %w", err)
	}
	return workspaceDir, nil
}

// PIDPath returns the full path to the PID file.
func PIDPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, pidFileName), nil
}

// LogPath returns the full path to the daemon log file.
func LogPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, logFileName), nil
}

// WritePID writes the current process PID to the PID file.
func WritePID() error {
	path, err := PIDPath()
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// RemovePID removes the PID file only if it contains the current process's PID.
// This prevents a failing instance from deleting another instance's PID file.
func RemovePID() {
	path, err := PIDPath()
	if err != nil {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	filePid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		os.Remove(path) // corrupt file, remove it
		return
	}
	if filePid == os.Getpid() {
		os.Remove(path)
	}
}

// ForceRemovePID removes the PID file unconditionally.
// Used by Stop() after killing another process.
func ForceRemovePID() {
	path, err := PIDPath()
	if err != nil {
		return
	}
	os.Remove(path)
}

// ReadPID reads the PID from the PID file. Returns 0 if the file does not exist.
func ReadPID() (int, error) {
	path, err := PIDPath()
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read PID file: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid PID file content: %w", err)
	}
	return pid, nil
}

// IsRunning checks whether a process with the given PID is alive.
func IsRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Use signal 0 to check liveness.
	err = proc.Signal(syscall.Signal(0))
	return err == nil
}

// Start launches the gateway as a background process by re-executing the
// current binary with the "gateway run" subcommand. Stdout/stderr are
// redirected to the daemon log file.
func Start() error {
	pid, err := ReadPID()
	if err != nil {
		return err
	}
	if IsRunning(pid) {
		return fmt.Errorf("gateway is already running (pid %d)", pid)
	}

	// Clean up stale PID file
	ForceRemovePID()

	logPath, err := LogPath()
	if err != nil {
		return err
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	execPath, err := currentExecutable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	cmd := exec.Command(execPath, "gateway", "run")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Detach the child from the parent process group so it survives parent exit.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Inherit the current environment so Telegram token and other env vars are available.
	cmd.Env = os.Environ()

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start gateway process: %w", err)
	}

	childPid := cmd.Process.Pid

	// Wait for the child in background so it doesn't become a zombie.
	childExited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		logFile.Close()
		close(childExited)
	}()

	// Probe: wait briefly and verify the child didn't crash on startup.
	// This catches common failures like "address already in use".
	select {
	case <-childExited:
		// Child already exited — almost certainly an error.
		lines := tailFile(logPath, 5)
		hint := ""
		if len(lines) > 0 {
			hint = "\n\n  recent log output:\n"
			for _, l := range lines {
				hint += "    " + l + "\n"
			}
		}
		return fmt.Errorf("gateway process exited immediately (pid %d)%s", childPid, hint)
	case <-time.After(startProbeDelay):
		// Still alive after probe delay — good.
	}

	if !IsRunning(childPid) {
		lines := tailFile(logPath, 5)
		hint := ""
		if len(lines) > 0 {
			hint = "\n\n  recent log output:\n"
			for _, l := range lines {
				hint += "    " + l + "\n"
			}
		}
		return fmt.Errorf("gateway process died shortly after start (pid %d)%s", childPid, hint)
	}

	// Persist the child PID so Stop/Status can find it later.
	pidPath, err := PIDPath()
	if err != nil {
		return fmt.Errorf("resolve PID path: %w", err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(childPid)), 0o644); err != nil {
		return fmt.Errorf("write PID file: %w", err)
	}

	fmt.Printf("%s gateway started %s\n", green("✓"), dim(fmt.Sprintf("(pid %d)", childPid)))
	fmt.Printf("  log: %s\n", logPath)
	return nil
}

// Stop sends SIGTERM to the running gateway process and waits for it to exit.
// Falls back to SIGKILL after a grace period.
func Stop() error {
	pid, err := ReadPID()
	if err != nil {
		return err
	}
	if pid == 0 {
		return fmt.Errorf("gateway is not running (no PID file)")
	}
	if !IsRunning(pid) {
		ForceRemovePID()
		return fmt.Errorf("gateway is not running (stale PID %d)", pid)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	// Graceful shutdown via SIGTERM.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM to %d: %w", pid, err)
	}
	fmt.Printf("  stopping gateway %s...\n", dim(fmt.Sprintf("(pid %d)", pid)))

	deadline := time.Now().Add(stopGraceTime)
	for time.Now().Before(deadline) {
		if !IsRunning(pid) {
			ForceRemovePID()
			fmt.Printf("%s gateway stopped\n", green("✓"))
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Force kill.
	fmt.Printf("  gateway did not exit in %s, sending SIGKILL...\n", stopGraceTime)
	if err := proc.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("send SIGKILL to %d: %w", pid, err)
	}

	// Wait for SIGKILL to take effect.
	for range 10 {
		time.Sleep(200 * time.Millisecond)
		if !IsRunning(pid) {
			ForceRemovePID()
			fmt.Printf("%s gateway killed\n", green("✓"))
			return nil
		}
	}

	ForceRemovePID()
	return fmt.Errorf("process %d could not be killed", pid)
}

// Restart stops the running gateway (if any) and starts a new instance.
func Restart() error {
	pid, _ := ReadPID()
	if IsRunning(pid) {
		if err := Stop(); err != nil {
			return fmt.Errorf("stop during restart: %w", err)
		}
		// Brief pause to let the OS fully release the listen socket.
		// This avoids "address already in use" when the new process starts.
		time.Sleep(500 * time.Millisecond)
	} else if pid > 0 {
		// Stale PID file — clean it up.
		ForceRemovePID()
	}

	// If no PID file but the gateway port is occupied (e.g. a previous instance
	// started before the PID-file fix), find and kill the orphan process.
	if pid == 0 || !IsRunning(pid) {
		if err := killOrphanOnPort(); err != nil {
			// Non-fatal: Start() will report "address already in use" if needed.
			_ = err
		}
	}

	return Start()
}

// killOrphanOnPort checks if the configured gateway address is already in use.
// If so, it tries to find the process via fuser/lsof and kill it.
func killOrphanOnPort() error {
	summary := loadConfigSummary()
	addr := summary.Address
	if addr == "" {
		addr = ":8080"
	}

	// Try to listen — if we can, port is free.
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		ln.Close()
		return nil // Port is free, nothing to do.
	}

	// Port is occupied. Try to find the PID using fuser.
	port := addr
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		port = addr[idx+1:]
	}

	out, fuserErr := exec.Command("fuser", port+"/tcp").Output()
	if fuserErr != nil || len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Errorf("port %s is in use but cannot identify the process", port)
	}

	orphanPid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return fmt.Errorf("parse fuser output %q: %w", string(out), err)
	}

	if !IsRunning(orphanPid) {
		return nil
	}

	fmt.Printf("  found orphan gateway on port %s %s\n", port, dim(fmt.Sprintf("(pid %d)", orphanPid)))

	proc, err := os.FindProcess(orphanPid)
	if err != nil {
		return err
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return err
	}

	deadline := time.Now().Add(stopGraceTime)
	for time.Now().Before(deadline) {
		if !IsRunning(orphanPid) {
			fmt.Printf("%s orphan gateway stopped\n", green("✓"))
			time.Sleep(300 * time.Millisecond) // let socket release
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Force kill.
	_ = proc.Signal(syscall.SIGKILL)
	time.Sleep(300 * time.Millisecond)
	fmt.Printf("%s orphan gateway killed\n", green("✓"))
	return nil
}

// Status prints the current gateway process status and runtime summary.
func Status() {
	pid, err := ReadPID()
	if err != nil {
		fmt.Printf("%s %v\n", red("✗"), err)
		return
	}

	if pid == 0 {
		fmt.Printf("%s gateway is not running %s\n",
			dim("•"), dim("(no PID file)"))
	} else if !IsRunning(pid) {
		fmt.Printf("%s gateway is not running %s\n",
			dim("•"), dim(fmt.Sprintf("(stale PID %d)", pid)))
	} else {
		uptime := processUptime(pid)
		if uptime != "" {
			fmt.Printf("%s gateway is running %s %s\n",
				green("✓"), dim(fmt.Sprintf("(pid %d)", pid)),
				dim("uptime "+uptime))
		} else {
			fmt.Printf("%s gateway is running %s\n",
				green("✓"), dim(fmt.Sprintf("(pid %d)", pid)))
		}
	}

	printConfigSummary()
	printLogSummary()
}

// printConfigSummary prints config file path, enabled channels, and listen address.
func printConfigSummary() {
	configPath, err := configFilePath()
	if err == nil {
		fmt.Printf("  config:   %s\n", configPath)
	}

	summary := loadConfigSummary()
	if summary.ConfigError != "" {
		fmt.Printf("  channels: %s\n",
			dim("unavailable ("+summary.ConfigError+")"))
	} else {
		printChannels(summary.Channels)
	}
	if summary.Address != "" {
		fmt.Printf("  address:  %s\n", summary.Address)
	}
}

func printChannels(channels []string) {
	if len(channels) == 0 {
		fmt.Printf("  channels: %s\n", dim("none"))
		return
	}
	if len(channels) == 1 {
		fmt.Printf("  channels: %s\n", channels[0])
		return
	}

	fmt.Println("  channels:")
	for _, ch := range channels {
		fmt.Printf("    - %s\n", ch)
	}
}

func printLogSummary() {
	logPath, err := LogPath()
	if err != nil {
		return
	}
	fmt.Printf("  log:      %s\n", logPath)
}

// configFilePath returns the config file path.
func configFilePath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "clawdex.json"), nil
}

type configSummary struct {
	Address     string
	Channels    []string
	ConfigError string
}

// loadConfigSummary reads the config file and returns the status summary.
func loadConfigSummary() configSummary {
	configPath, err := configFilePath()
	if err != nil {
		return configSummary{}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return configSummary{}
	}

	var raw struct {
		Channels map[string]json.RawMessage `json:"channels"`
		Gateway  struct {
			Address string `json:"address"`
		} `json:"gateway"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return configSummary{ConfigError: "invalid config file"}
	}

	summary := configSummary{Address: raw.Gateway.Address}
	if summary.Address == "" {
		summary.Address = ":8080"
	}

	channels, err := summarizeChannels(raw.Channels)
	if err != nil {
		summary.ConfigError = "invalid channels"
		return summary
	}
	summary.Channels = channels
	return summary
}

func summarizeChannels(rawChannels map[string]json.RawMessage) ([]string, error) {
	if len(rawChannels) == 0 {
		return nil, nil
	}

	names := make([]string, 0, len(rawChannels))
	for name := range rawChannels {
		names = append(names, name)
	}
	sort.Strings(names)

	channels := make([]string, 0, len(rawChannels))
	for _, name := range names {
		raw := rawChannels[name]
		label, ok, err := summarizeChannel(name, raw)
		if err != nil {
			return nil, err
		}
		if ok {
			channels = append(channels, label)
		}
	}
	return channels, nil
}

func summarizeChannel(name string, raw json.RawMessage) (string, bool, error) {
	var meta struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", false, err
	}

	switch meta.Type {
	case "telegram":
		var tg struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.Unmarshal(raw, &tg); err != nil {
			return "", false, err
		}
		enabled := tg.Enabled == nil || *tg.Enabled
		if !enabled {
			return "", false, nil
		}
		return green("telegram") + dim("/"+name), true, nil
	case "wecom":
		var wc struct {
			Enabled        *bool  `json:"enabled"`
			ConnectionMode string `json:"connection_mode"`
			WebhookPath    string `json:"webhook_path"`
		}
		if err := json.Unmarshal(raw, &wc); err != nil {
			return "", false, err
		}
		enabled := wc.Enabled != nil && *wc.Enabled
		if !enabled {
			return "", false, nil
		}

		mode := wc.ConnectionMode
		if mode == "" {
			mode = "webhook"
		}
		label := green("wecom") + dim("/"+name)
		if mode == "websocket" {
			label += " " + dim("(websocket)")
			return label, true, nil
		}
		if wc.WebhookPath != "" {
			path := displayWebhookPath(wc.WebhookPath)
			label += " " + dim("(webhook "+path+")")
			return label, true, nil
		}
		label += " " + dim("(webhook)")
		return label, true, nil
	default:
		if meta.Type == "" {
			return "", false, fmt.Errorf("missing channel type for %q", name)
		}
		return green(meta.Type) + dim("/"+name), true, nil
	}
}

func displayWebhookPath(raw string) string {
	resolved, err := secret.Resolve(raw)
	if err == nil && resolved != "" {
		return resolved
	}
	if secret.IsRef(raw) {
		return secret.Describe(raw)
	}
	return raw
}

// processUptime returns a human-readable uptime for the given PID, or empty on error.
func processUptime(pid int) string {
	statPath := fmt.Sprintf("/proc/%d/stat", pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		return ""
	}
	// Parse field 22 (starttime in clock ticks).
	// Format: pid (comm) state ppid ... field22 ...
	// Find the closing ')' to skip the comm field which may contain spaces.
	content := string(data)
	idx := strings.LastIndex(content, ")")
	if idx < 0 || idx+2 >= len(content) {
		return ""
	}
	fields := strings.Fields(content[idx+2:])
	if len(fields) < 20 {
		return ""
	}
	startTicks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return ""
	}

	// Read system uptime.
	uptimeData, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return ""
	}
	uptimeParts := strings.Fields(string(uptimeData))
	if len(uptimeParts) == 0 {
		return ""
	}
	systemUptime, err := strconv.ParseFloat(uptimeParts[0], 64)
	if err != nil {
		return ""
	}

	// Clock ticks per second (usually 100 on Linux).
	clkTck := 100.0
	processStart := float64(startTicks) / clkTck
	elapsed := time.Duration((systemUptime - processStart) * float64(time.Second))

	if elapsed < 0 {
		return ""
	}

	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm%ds", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(elapsed.Hours()), int(elapsed.Minutes())%60)
	default:
		days := int(elapsed.Hours()) / 24
		hours := int(elapsed.Hours()) % 24
		return fmt.Sprintf("%dd%dh", days, hours)
	}
}

// tailFile reads the last n lines from a file.
func tailFile(path string, n int) []string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}

	content := strings.TrimRight(string(data), "\n")
	lines := strings.Split(content, "\n")

	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// LogsOption is a functional option for Logs.
type LogsOption func(*logsOptions)

type logsOptions struct {
	follow bool
	lines  int
}

// WithFollow enables tail -f behavior for log output.
func WithFollow(follow bool) LogsOption {
	return func(o *logsOptions) {
		o.follow = follow
	}
}

// WithLines sets the number of lines to show (default 40).
func WithLines(n int) LogsOption {
	return func(o *logsOptions) {
		o.lines = n
	}
}

// Logs prints gateway logs. It detects whether systemd or the daemon log file
// should be used.
func Logs(opts ...LogsOption) error {
	var lo logsOptions
	for _, opt := range opts {
		opt(&lo)
	}

	if lo.lines <= 0 {
		lo.lines = 40
	}

	// Detect systemd: if the unit is active, use journalctl.
	if isSystemdActive() {
		return logsJournalctl(lo.follow, lo.lines)
	}

	// Fall back to the daemon log file.
	logPath, err := LogPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		return fmt.Errorf("no log file found at %s\n\nStart the gateway first: clawdex gateway start", logPath)
	}

	if lo.follow {
		return logsTailFollow(logPath, lo.lines)
	}

	lines := tailFile(logPath, lo.lines)
	if len(lines) == 0 {
		fmt.Println("(log file is empty)")
		return nil
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

// isSystemdActive checks if the clawdex-gateway systemd user service is active.
func isSystemdActive() bool {
	cmd := exec.Command("systemctl", "--user", "is-active", "--quiet", serviceName+".service")
	return cmd.Run() == nil
}

// logsJournalctl execs into journalctl for systemd logs.
func logsJournalctl(follow bool, n int) error {
	args := []string{"--user", "-u", serviceName + ".service",
		"-n", strconv.Itoa(n), "--no-pager"}
	if follow {
		args = append(args, "-f")
	}
	bin, err := exec.LookPath("journalctl")
	if err != nil {
		return fmt.Errorf("journalctl not found: %w", err)
	}
	return syscall.Exec(bin, append([]string{"journalctl"}, args...), os.Environ())
}

// logsTailFollow execs into tail -f for the daemon log file.
func logsTailFollow(logPath string, n int) error {
	bin, err := exec.LookPath("tail")
	if err != nil {
		return fmt.Errorf("tail not found: %w", err)
	}
	args := []string{"tail", "-n", strconv.Itoa(n), "-f", logPath}
	return syscall.Exec(bin, args, os.Environ())
}
