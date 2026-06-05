package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/clawdex/internal/app"
	"github.com/Rememorio/clawdex/internal/config"
	"github.com/Rememorio/clawdex/internal/daemon"
	"github.com/Rememorio/clawdex/internal/doctor"
	"github.com/Rememorio/clawdex/internal/onboard"
	"github.com/Rememorio/clawdex/internal/termcolor"
	"github.com/Rememorio/clawdex/internal/updater"
	"github.com/Rememorio/clawdex/internal/version"
)

var term = termcolor.New(os.Stderr)

func bold(s string) string {
	return term.Bold(s)
}

func red(s string) string {
	return term.Red(s)
}

func green(s string) string {
	return term.Green(s)
}

func yellow(s string) string {
	return term.Yellow(s)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "%s\n", bold("clawdex")+" — codex gateway for messaging channels")
	fmt.Fprintln(os.Stderr, "--------")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "%s\n", bold("Usage:"))
	fmt.Fprintln(os.Stderr, "  clawdex version             Show version")
	fmt.Fprintln(os.Stderr, "  clawdex update              Update to latest GitHub release")
	fmt.Fprintln(os.Stderr, "  clawdex update --check      Check whether an update is available")
	fmt.Fprintln(os.Stderr, "  clawdex onboard             Interactive setup wizard")
	fmt.Fprintln(os.Stderr, "  clawdex daemon install      Install systemd user service")
	fmt.Fprintln(os.Stderr, "  clawdex daemon uninstall    Remove systemd user service")
	fmt.Fprintln(os.Stderr, "  clawdex daemon status       Show systemd service status")
	fmt.Fprintln(os.Stderr, "  clawdex gateway start       Start as background daemon")
	fmt.Fprintln(os.Stderr, "  clawdex gateway stop        Stop the daemon")
	fmt.Fprintln(os.Stderr, "  clawdex gateway restart     Restart the daemon")
	fmt.Fprintln(os.Stderr, "  clawdex gateway status      Show process status")
	fmt.Fprintln(os.Stderr, "  clawdex gateway run         Run in foreground")
	fmt.Fprintln(os.Stderr, "  clawdex gateway logs        Show recent logs")
	fmt.Fprintln(os.Stderr, "  clawdex gateway logs -f     Follow log output")
	fmt.Fprintln(os.Stderr, "  clawdex pairing list        List pending pairing requests")
	fmt.Fprintln(os.Stderr, "  clawdex pairing approve <CODE>  Approve a pairing request")
	fmt.Fprintln(os.Stderr, "  clawdex doctor              Check configuration health")
	fmt.Fprintln(os.Stderr, "  clawdex doctor --fix        Check and auto-fix problems")
	fmt.Fprintln(os.Stderr, "  clawdex config list         Show all config values")
	fmt.Fprintln(os.Stderr, "  clawdex config get <KEY>    Get a config value")
	fmt.Fprintln(os.Stderr, "  clawdex config set <KEY> <VALUE>  Set a config value")
	fmt.Fprintln(os.Stderr, "  clawdex config file         Show config file path")
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "version":
		fmt.Println(version.Version)
	case "update":
		err = runUpdateCommand()
	case "onboard":
		err = runOnboardCommand()
	case "daemon":
		err = runDaemonCommand()
	case "gateway":
		err = runGatewayCommand()
	case "pairing":
		err = runPairingCommand()
	case "doctor":
		err = runDoctorCommand()
	case "config":
		err = runConfigCommand()
	default:
		fmt.Fprintf(os.Stderr, "%s unknown command: %s\n\n", red("✗"), os.Args[1])
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", red("✗"), err)
		os.Exit(1)
	}
}

func runUpdateCommand() error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	checkOnly := fs.Bool("check", false, "check whether an update is available")
	force := fs.Bool("force", false, "install latest release even when versions match")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument: %s", fs.Arg(0))
	}

	return updater.Run(context.Background(), updater.Options{
		CurrentVersion: version.Version,
		CheckOnly:      *checkOnly,
		Force:          *force,
		Stdout:         os.Stdout,
	})
}

func runOnboardCommand() error {
	installDaemon := false
	for _, arg := range os.Args[2:] {
		if arg == "--install-daemon" {
			installDaemon = true
		}
	}
	return onboard.Run(onboard.WithInstallDaemon(installDaemon))
}

func runDaemonCommand() error {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  clawdex daemon install     Install systemd user service")
		fmt.Fprintln(os.Stderr, "  clawdex daemon uninstall   Remove systemd user service")
		fmt.Fprintln(os.Stderr, "  clawdex daemon status      Show systemd service status")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "install":
		return daemon.Install()
	case "uninstall":
		return daemon.Uninstall()
	case "status":
		daemon.SystemdStatus()
		return nil
	default:
		fmt.Fprintf(os.Stderr, "%s unknown daemon command: %s\n\n", red("✗"), os.Args[2])
		printUsage()
		os.Exit(1)
	}
	return nil
}

func runGatewayCommand() error {
	if len(os.Args) < 3 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[2] {
	case "start":
		if _, err := config.Load(); err != nil {
			return fmt.Errorf("config check failed: %w", err)
		}
		return daemon.Start()
	case "stop":
		return daemon.Stop()
	case "restart":
		if _, err := config.Load(); err != nil {
			return fmt.Errorf("config check failed: %w", err)
		}
		return daemon.Restart()
	case "status":
		daemon.Status()
		return nil
	case "run":
		return app.RunGateway()
	case "logs":
		return runGatewayLogs()
	default:
		fmt.Fprintf(os.Stderr, "%s unknown gateway command: %s\n\n", red("✗"), os.Args[2])
		printUsage()
		os.Exit(1)
	}
	return nil
}

func runGatewayLogs() error {
	follow := false
	n := 40
	// Parse flags: -f, -n <num>
	for i := 3; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "-f", "--follow":
			follow = true
		case "-n":
			if i+1 >= len(os.Args) {
				return fmt.Errorf("-n requires a number argument")
			}
			i++
			val, err := strconv.Atoi(os.Args[i])
			if err != nil || val < 1 {
				return fmt.Errorf("invalid -n value: %s", os.Args[i])
			}
			n = val
		default:
			return fmt.Errorf("unknown flag: %s\n\nUsage: clawdex gateway logs [-f] [-n <lines>]", os.Args[i])
		}
	}
	return daemon.Logs(daemon.WithFollow(follow), daemon.WithLines(n))
}

func runPairingCommand() error {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  clawdex pairing list             List pending pairing requests")
		fmt.Fprintln(os.Stderr, "  clawdex pairing approve <CODE>   Approve a pairing request")
		os.Exit(1)
	}

	addr, err := gatewayAddress()
	if err != nil {
		return err
	}

	switch os.Args[2] {
	case "list":
		return pairingList(addr)
	case "approve":
		if len(os.Args) < 4 {
			return fmt.Errorf("usage: clawdex pairing approve <CODE>")
		}
		return pairingApprove(addr, strings.ToUpper(strings.TrimSpace(os.Args[3])))
	default:
		fmt.Fprintf(os.Stderr, "%s unknown pairing command: %s\n\n", red("✗"), os.Args[2])
		printUsage()
		os.Exit(1)
	}
	return nil
}

func gatewayAddress() (string, error) {
	// Try loading from config file; fall back to default.
	fileCfg, err := onboard.LoadFileConfig()
	if err != nil {
		return "http://localhost:8080", nil
	}
	addr := fileCfg.Gateway.Address
	if addr == "" {
		addr = ":8080"
	}
	// Normalize to a full URL.
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	return "http://" + addr, nil
}

type pairingEntry struct {
	Code      string `json:"code"`
	Channel   string `json:"channel"`
	SenderID  int64  `json:"sender_id"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}

func pairingList(baseURL string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(baseURL + "/pairing/list")
	if err != nil {
		return fmt.Errorf("failed to connect to gateway: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway returned status %d: %s", resp.StatusCode, string(body))
	}

	var entries []pairingEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if len(entries) == 0 {
		fmt.Println("No pending pairing requests.")
		return nil
	}

	fmt.Printf("%-8s %-10s %-14s %-20s %s\n", "CODE", "CHANNEL", "SENDER ID", "USERNAME", "CREATED")
	for _, e := range entries {
		fmt.Printf("%-8s %-10s %-14d %-20s %s\n", e.Code, e.Channel, e.SenderID, e.Username, e.CreatedAt)
	}
	return nil
}

func pairingApprove(baseURL, code string) error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(baseURL+"/pairing/approve?code="+code, "application/json", nil)
	if err != nil {
		return fmt.Errorf("failed to connect to gateway: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	var result struct {
		OK       bool   `json:"ok"`
		Channel  string `json:"channel"`
		SenderID int64  `json:"sender_id"`
		Username string `json:"username"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	if !result.OK {
		errMsg := result.Error
		if errMsg == "" {
			errMsg = "unknown error"
		}
		return fmt.Errorf("approval failed: %s", errMsg)
	}

	name := result.Username
	if name == "" {
		name = fmt.Sprintf("user %d", result.SenderID)
	}
	ch := result.Channel
	if ch == "" {
		ch = "unknown"
	}
	fmt.Printf("Approved %s (ID: %d, channel: %s)\n", name, result.SenderID, ch)
	return nil
}

func runDoctorCommand() error {
	fix := false
	for _, arg := range os.Args[2:] {
		if arg == "--fix" {
			fix = true
		}
	}

	fmt.Println(bold("clawdex doctor"))
	fmt.Println("──────────────")

	checks := doctor.Run(doctor.WithFix(fix))

	var passes, warns, fails int
	for _, c := range checks {
		var icon, label string
		switch c.Status {
		case doctor.Pass:
			passes++
			icon = green("✓")
			label = c.Message
		case doctor.Warn:
			warns++
			icon = yellow("!")
			label = c.Message
		case doctor.Fail:
			fails++
			icon = red("✗")
			label = c.Message
		}
		fmt.Fprintf(os.Stderr, "  %s %-18s %s\n", icon, c.Name, label)
		if c.Fixed {
			fmt.Fprintf(os.Stderr, "    → fixed\n")
		}
	}

	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "%d checks passed, %d warnings, %d errors\n", passes, warns, fails)

	if fails > 0 {
		return fmt.Errorf("%d check(s) failed", fails)
	}
	return nil
}

func runConfigCommand() error {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  clawdex config list              Show all config values")
		fmt.Fprintln(os.Stderr, "  clawdex config get <KEY>         Get a config value")
		fmt.Fprintln(os.Stderr, "  clawdex config set <KEY> <VALUE> Set a config value")
		fmt.Fprintln(os.Stderr, "  clawdex config file              Show config file path")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "WeCom keys use dot-separated instance index or name:")
		fmt.Fprintln(os.Stderr, "  wecom.0.token                    Access first instance by index")
		fmt.Fprintln(os.Stderr, "  wecom.my-bot.token               Access instance by name")
		fmt.Fprintln(os.Stderr, "  wecom.token                      Shorthand for wecom.0.token")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "list":
		return app.ConfigList()
	case "get":
		if len(os.Args) < 4 {
			return fmt.Errorf("usage: clawdex config get <KEY>")
		}
		return app.ConfigGet(os.Args[3])
	case "set":
		if len(os.Args) < 5 {
			return fmt.Errorf("usage: clawdex config set <KEY> <VALUE>")
		}
		return app.ConfigSet(os.Args[3], strings.Join(os.Args[4:], " "))
	case "file":
		return app.ConfigFile()
	default:
		fmt.Fprintf(os.Stderr, "%s unknown config command: %s\n\n", red("✗"), os.Args[2])
		printUsage()
		os.Exit(1)
	}
	return nil
}
