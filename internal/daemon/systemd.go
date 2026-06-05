package daemon

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/Rememorio/clawdex/internal/secret"
)

const serviceName = "clawdex-gateway"

var autoSyncEnvNames = []string{
	"PATH",
}

var unitTemplate = template.Must(template.New("unit").Parse(`[Unit]
Description=clawdex gateway
After=network-online.target
Wants=network-online.target

[Service]
ExecStart={{.ExecStart}} gateway run
Restart=on-failure
RestartSec=5
WorkingDirectory=%h/.clawdex
EnvironmentFile=-%h/.clawdex/env

[Install]
WantedBy=default.target
`))

// unitFilePath returns ~/.config/systemd/user/clawdex-gateway.service.
func unitFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "systemd", "user", serviceName+".service"), nil
}

// generateUnit renders the systemd unit file with the given executable path.
func generateUnit(execPath string) (string, error) {
	var buf bytes.Buffer
	if err := unitTemplate.Execute(&buf, struct{ ExecStart string }{ExecStart: execPath}); err != nil {
		return "", fmt.Errorf("render unit template: %w", err)
	}
	return buf.String(), nil
}

// runCmd executes a command and returns stderr on failure.
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
	}
	return nil
}

// envTemplate is the content written to ~/.clawdex/env if it doesn't exist.
const envTemplate = `# clawdex environment variables
# Uncomment and set the variables needed for your channels.

# Codex CLI resolution (useful for systemd user services).
#PATH=/path/to/codex/bin:/usr/local/bin:/usr/bin:/bin

# Telegram (if using Telegram)
#TELEGRAM_BOT_TOKEN=123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11

# WeCom Webhook mode (if using webhook)
#WECOM_TOKEN=your-callback-token
#WECOM_ENCODING_AES_KEY=your-43-character-encoding-aes-key
#WECOM_WEBHOOK_PATH=/wecom/webhook

# WeCom WebSocket mode (if using websocket)
#WECOM_BOTID=your-bot-id
#WECOM_SECRET=your-websocket-secret
`

// ensureEnvTemplate creates ~/.clawdex/env with a template if it doesn't exist.
func ensureEnvTemplate() error {
	dir, err := DataDir()
	if err != nil {
		return err
	}
	envPath := filepath.Join(dir, "env")

	// Check if file already exists.
	if _, err := os.Stat(envPath); err == nil {
		return nil
	}

	// Create the template file.
	if err := os.WriteFile(envPath, []byte(envTemplate), 0o600); err != nil {
		return fmt.Errorf("create env template: %w", err)
	}

	fmt.Printf("%s created env template: %s\n", cyan("ℹ"), envPath)
	fmt.Println("  Edit this file to set your credentials.")
	return nil
}

// syncEnvFile scans the config for ${VAR} references and appends any variables
// that are present in the current shell environment but missing from the env
// file. It also syncs runtime variables like PATH so the systemd service can
// resolve external CLIs such as codex.
func syncEnvFile() error {
	dir, err := DataDir()
	if err != nil {
		return err
	}
	envPath := filepath.Join(dir, "env")
	configPath := filepath.Join(dir, "clawdex.json")

	refSet := make(map[string]bool)

	// Read config to find ${VAR} references.
	configData, err := os.ReadFile(configPath)
	switch {
	case err == nil:
		for _, name := range secret.FindEnvRefs(configData) {
			refSet[name] = true
		}
	case os.IsNotExist(err):
		// No config yet. We can still sync runtime variables like PATH.
	default:
		return fmt.Errorf("read config: %w", err)
	}
	for _, name := range autoSyncEnvNames {
		refSet[name] = true
	}
	if len(refSet) == 0 {
		return nil
	}

	// Parse existing env file to find already-defined variables.
	existing := parseEnvFileKeys(envPath)
	refs := make([]string, 0, len(refSet))
	for name := range refSet {
		refs = append(refs, name)
	}
	sort.Strings(refs)

	// Collect missing variables that are set in the current environment.
	var lines []string
	for _, name := range refs {
		if existing[name] {
			continue
		}
		val := os.Getenv(name)
		if val == "" {
			continue
		}
		lines = append(lines, name+"="+val)
		fmt.Printf("%s synced %s from shell to env file\n", cyan("ℹ"), name)
	}

	if len(lines) == 0 {
		return nil
	}

	// Append to the env file.
	f, err := os.OpenFile(envPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open env file: %w", err)
	}
	defer f.Close()

	content := "\n# Auto-synced by daemon install\n" +
		strings.Join(lines, "\n") + "\n"
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("append to env file: %w", err)
	}
	return nil
}

// parseEnvFileKeys reads an env file and returns a set of variable names that
// have actual (uncommented) definitions. Commented-out lines like
// "#TELEGRAM_BOT_TOKEN=..." are NOT considered defined, so syncEnvFile can
// replace them with real values from the shell.
func parseEnvFileKeys(path string) map[string]bool {
	keys := make(map[string]bool)
	f, err := os.Open(path)
	if err != nil {
		return keys
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Skip blank lines and comments.
		if line == "" || line[0] == '#' {
			continue
		}
		// Extract KEY from KEY=VALUE.
		if idx := strings.IndexByte(line, '='); idx > 0 {
			key := strings.TrimSpace(line[:idx])
			if key != "" {
				keys[key] = true
			}
		}
	}
	return keys
}

// Install creates and starts a systemd user service for the gateway.
func Install() error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	unitPath, err := unitFilePath()
	if err != nil {
		return err
	}

	// Stop any PID-file-managed gateway process before starting the systemd
	// service, so they don't compete for the same port.
	if pid, err := ReadPID(); err == nil && pid != 0 && IsRunning(pid) {
		fmt.Printf("  stopping existing gateway %s...\n", dim(fmt.Sprintf("(pid %d)", pid)))
		_ = Stop()
	}

	// Stop any existing systemd service so the restart below doesn't race
	// with a still-running instance (Restart=on-failure may have respawned it).
	_ = runCmd("systemctl", "--user", "stop", serviceName+".service")

	// Ensure the directory exists.
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return fmt.Errorf("create systemd user dir: %w", err)
	}

	// Ensure env template file exists for systemd EnvironmentFile.
	if err := ensureEnvTemplate(); err != nil {
		return err
	}

	// Sync ${VAR} references from config into env file using current shell.
	if err := syncEnvFile(); err != nil {
		return err
	}

	// Generate unit content.
	content, err := generateUnit(execPath)
	if err != nil {
		return err
	}

	// Atomic write: temp file + rename.
	dir := filepath.Dir(unitPath)
	tmp, err := os.CreateTemp(dir, ".clawdex-unit-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write unit file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close unit file: %w", err)
	}
	if err := os.Rename(tmpPath, unitPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("install unit file: %w", err)
	}

	// Reload, enable, and start.
	if err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if err := runCmd("systemctl", "--user", "enable", serviceName+".service"); err != nil {
		return fmt.Errorf("enable service: %w", err)
	}
	if err := runCmd("systemctl", "--user", "restart", serviceName+".service"); err != nil {
		return fmt.Errorf("start service: %w", err)
	}

	// Best-effort: enable lingering so the service survives logout.
	u, err := user.Current()
	if err == nil {
		_ = runCmd("loginctl", "enable-linger", u.Username)
	}

	fmt.Printf("%s systemd user service installed and started\n", green("✓"))
	fmt.Printf("  unit: %s\n", unitPath)
	fmt.Printf("  logs: %s\n", dim("journalctl --user -u "+serviceName+" -f"))
	return nil
}

// SystemdStatus prints the current systemd user service status.
func SystemdStatus() {
	unitPath, err := unitFilePath()
	if err != nil {
		fmt.Printf("%s cannot resolve unit file path: %v\n", red("✗"), err)
		return
	}

	// Check if the unit file exists.
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		fmt.Printf("%s systemd user service is not installed\n", dim("•"))
		fmt.Println("  run: clawdex daemon install")
		return
	}

	// Query systemctl for active state.
	out, err := exec.Command("systemctl", "--user", "is-active", serviceName+".service").Output()
	state := strings.TrimSpace(string(out))
	if err != nil && state == "" {
		state = "unknown"
	}

	switch state {
	case "active":
		fmt.Printf("%s systemd service is active\n", green("✓"))
	case "inactive":
		fmt.Printf("%s systemd service is inactive (stopped)\n", dim("•"))
	case "failed":
		fmt.Printf("%s systemd service has failed\n", red("✗"))
	default:
		fmt.Printf("%s systemd service state: %s\n", dim("•"), state)
	}

	fmt.Printf("  unit: %s\n", unitPath)

	// Show enabled/disabled state.
	enableOut, _ := exec.Command("systemctl", "--user", "is-enabled", serviceName+".service").Output()
	enabled := strings.TrimSpace(string(enableOut))
	if enabled != "" {
		fmt.Printf("  boot: %s\n", enabled)
	}

	fmt.Printf("  logs: %s\n", dim("journalctl --user -u "+serviceName+" -f"))
}

// Uninstall stops and removes the systemd user service.
func Uninstall() error {
	unitPath, err := unitFilePath()
	if err != nil {
		return err
	}

	// Best-effort stop and disable.
	_ = runCmd("systemctl", "--user", "stop", serviceName+".service")
	_ = runCmd("systemctl", "--user", "disable", serviceName+".service")

	// Remove the unit file.
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file: %w", err)
	}

	// Reload so systemd forgets the unit.
	if err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}

	fmt.Printf("%s systemd user service removed\n", green("✓"))
	return nil
}
