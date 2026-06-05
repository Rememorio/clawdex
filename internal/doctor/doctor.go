// Package doctor provides health checks and auto-fix for clawdex configuration.
package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/Rememorio/clawdex/internal/codex"
	"github.com/Rememorio/clawdex/internal/daemon"
	"github.com/Rememorio/clawdex/internal/onboard"
	"github.com/Rememorio/clawdex/internal/secret"
)

// Status represents the outcome of a single check.
type Status int

const (
	Pass Status = iota
	Warn
	Fail
)

// Check holds the result of a single diagnostic check.
type Check struct {
	Name    string
	Status  Status
	Message string
	Fixed   bool // true if auto-fixed during this run
}

// checkFunc signature: receives fix flag, returns check result.
type checkFunc func(fix bool) Check

// RunOption is a functional option for Run.
type RunOption func(*runOptions)

type runOptions struct {
	fix bool
}

// WithFix configures whether to auto-fix problems.
func WithFix(fix bool) RunOption {
	return func(o *runOptions) {
		o.fix = fix
	}
}

// Run executes all checks in order and returns the results.
func Run(opts ...RunOption) []Check {
	var ro runOptions
	for _, opt := range opts {
		opt(&ro)
	}

	checks := []checkFunc{
		checkConfigExists,
		checkConfigSyntax,
		checkBotTokenResolves,
		checkBotTokenValid,
		checkCodexCLI,
		checkWorkDir,
		checkSandbox,
		checkDMPolicy,
		checkStreaming,
		checkChunkMode,
		checkStalePID,
		checkGatewayHealth,
		checkDMPolicyOpen,
		checkDataDirWritable,
	}

	results := make([]Check, 0, len(checks))
	for _, fn := range checks {
		results = append(results, fn(ro.fix))
	}
	return results
}

// loadedConfig caches the loaded config for checks that need it.
// We load once and share across checks.
var loadedConfig *onboard.FileConfig
var loadedConfigErr error
var configPath string

func ensureConfig() (*onboard.FileConfig, string, error) {
	if loadedConfig != nil || loadedConfigErr != nil {
		return loadedConfig, configPath, loadedConfigErr
	}
	p, err := onboard.ConfigPath()
	if err != nil {
		loadedConfigErr = err
		return nil, "", err
	}
	configPath = p
	if _, err := os.Stat(p); os.IsNotExist(err) {
		loadedConfigErr = fmt.Errorf("config file not found: %s", p)
		return nil, p, loadedConfigErr
	}
	cfg, err := onboard.LoadFileConfigFrom(p)
	if err != nil {
		loadedConfigErr = err
		return nil, p, err
	}
	loadedConfig = cfg
	return cfg, p, nil
}

// ResetState clears cached state between runs (useful for tests).
func ResetState() {
	loadedConfig = nil
	loadedConfigErr = nil
	configPath = ""
}

// ── Individual checks ──

func checkConfigExists(fix bool) Check {
	p, err := onboard.ConfigPath()
	if err != nil {
		return Check{Name: "Config file", Status: Fail, Message: err.Error()}
	}
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return Check{Name: "Config file", Status: Fail, Message: fmt.Sprintf("not found — run `clawdex onboard` to create %s", p)}
	}
	return Check{Name: "Config file", Status: Pass, Message: p}
}

func checkConfigSyntax(fix bool) Check {
	_, p, err := ensureConfig()
	if err != nil {
		return Check{Name: "Config syntax", Status: Fail, Message: err.Error()}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return Check{Name: "Config syntax", Status: Fail, Message: err.Error()}
	}
	if !json.Valid(data) {
		return Check{Name: "Config syntax", Status: Fail, Message: "invalid JSON"}
	}
	return Check{Name: "Config syntax", Status: Pass, Message: "valid JSON"}
}

func checkBotTokenResolves(fix bool) Check {
	cfg, _, err := ensureConfig()
	if err != nil {
		return Check{Name: "Bot token", Status: Fail, Message: err.Error()}
	}
	ch := getTelegramChannel(cfg)
	if ch.BotToken == "" {
		return Check{Name: "Bot token", Status: Fail, Message: "bot_token not set in config"}
	}
	_, err = secret.Resolve(ch.BotToken)
	if err != nil {
		return Check{Name: "Bot token", Status: Fail, Message: err.Error()}
	}
	return Check{Name: "Bot token", Status: Pass, Message: "resolves OK"}
}

func checkBotTokenValid(fix bool) Check {
	cfg, _, err := ensureConfig()
	if err != nil {
		return Check{Name: "Bot verified", Status: Fail, Message: err.Error()}
	}
	ch := getTelegramChannel(cfg)
	token, err := secret.Resolve(ch.BotToken)
	if err != nil {
		return Check{Name: "Bot verified", Status: Fail, Message: "token does not resolve"}
	}
	if token == "" {
		return Check{Name: "Bot verified", Status: Fail, Message: "token is empty"}
	}

	info, err := verifyBotToken(token)
	if err != nil {
		return Check{Name: "Bot verified", Status: Fail, Message: err.Error()}
	}
	return Check{Name: "Bot verified", Status: Pass, Message: fmt.Sprintf("@%s (id: %d)", info.Username, info.ID)}
}

// getTelegramChannel returns the first telegram channel config or an empty one.
func getTelegramChannel(cfg *onboard.FileConfig) onboard.TelegramChannelConfig {
	if cfg.Channels == nil {
		return onboard.TelegramChannelConfig{}
	}
	for _, raw := range cfg.Channels {
		chType, _ := onboard.ChannelType(raw)
		if chType == "telegram" {
			var ch onboard.TelegramChannelConfig
			if err := json.Unmarshal(raw, &ch); err == nil {
				return ch
			}
		}
	}
	return onboard.TelegramChannelConfig{}
}

// getTelegramChannelName returns the name of the first telegram channel, or empty string.
func getTelegramChannelName(cfg *onboard.FileConfig) string {
	if cfg.Channels == nil {
		return ""
	}
	for name, raw := range cfg.Channels {
		chType, _ := onboard.ChannelType(raw)
		if chType == "telegram" {
			return name
		}
	}
	return ""
}

type botInfo struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// verifyBotToken calls Telegram getMe to validate the token.
func verifyBotToken(token string) (*botInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("https://api.telegram.org/bot" + token + "/getMe")
	if err != nil {
		return nil, fmt.Errorf("telegram API request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var result struct {
		OK          bool    `json:"ok"`
		Description string  `json:"description,omitempty"`
		Result      botInfo `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if !result.OK {
		desc := result.Description
		if desc == "" {
			desc = "unknown error"
		}
		return nil, fmt.Errorf("telegram rejected token: %s", desc)
	}
	return &result.Result, nil
}

func checkCodexCLI(fix bool) Check {
	cmd := exec.Command("codex", "--version")
	cmd.Env = codex.CleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Check{Name: "Codex CLI", Status: Fail, Message: "codex not found or not working"}
	}
	return Check{Name: "Codex CLI", Status: Pass, Message: strings.TrimSpace(string(out))}
}

func checkWorkDir(fix bool) Check {
	cfg, _, err := ensureConfig()
	if err != nil {
		return Check{Name: "Work directory", Status: Fail, Message: err.Error()}
	}
	dir := cfg.Codex.WorkDir
	if dir == "" {
		dir, err = daemon.WorkspaceDir()
		if err != nil {
			return Check{Name: "Work directory", Status: Fail, Message: err.Error()}
		}
		return Check{Name: "Work directory", Status: Pass, Message: dir + " (default)"}
	}
	stat, err := os.Stat(dir)
	if err != nil {
		return Check{Name: "Work directory", Status: Fail, Message: err.Error()}
	}
	if !stat.IsDir() {
		return Check{Name: "Work directory", Status: Fail, Message: dir + " is not a directory"}
	}
	return Check{Name: "Work directory", Status: Pass, Message: dir}
}

var validSandbox = map[string]bool{
	"read-only": true, "workspace-write": true, "danger-full-access": true,
}

func checkSandbox(fix bool) Check {
	cfg, path, err := ensureConfig()
	if err != nil {
		return Check{Name: "Sandbox", Status: Fail, Message: err.Error()}
	}
	val := cfg.Codex.Sandbox
	if val == "" {
		return Check{Name: "Sandbox", Status: Pass, Message: "workspace-write (default)"}
	}
	if validSandbox[val] {
		return Check{Name: "Sandbox", Status: Pass, Message: val}
	}
	if fix {
		cfg.Codex.Sandbox = "workspace-write"
		if err := onboard.SaveFileConfigTo(cfg, path); err != nil {
			return Check{Name: "Sandbox", Status: Fail, Message: fmt.Sprintf("invalid value %q; fix failed: %v", val, err)}
		}
		return Check{Name: "Sandbox", Status: Fail, Message: fmt.Sprintf("invalid value %q", val), Fixed: true}
	}
	return Check{Name: "Sandbox", Status: Fail, Message: fmt.Sprintf("invalid value %q", val)}
}

var validDMPolicy = map[string]bool{
	"open": true, "pairing": true, "allowlist": true,
}

func checkDMPolicy(fix bool) Check {
	cfg, path, err := ensureConfig()
	if err != nil {
		return Check{Name: "DM policy", Status: Fail, Message: err.Error()}
	}
	ch := getTelegramChannel(cfg)
	val := ch.DMPolicy
	if val == "" {
		return Check{Name: "DM policy", Status: Pass, Message: "pairing (default)"}
	}
	if validDMPolicy[val] {
		return Check{Name: "DM policy", Status: Pass, Message: val}
	}
	if fix {
		if cfg.Channels == nil {
			cfg.Channels = make(map[string]json.RawMessage)
		}
		name := getTelegramChannelName(cfg)
		if name == "" {
			name = "telegram"
		}
		// Get existing or create new.
		var tgCh onboard.TelegramChannelConfig
		if raw, exists := cfg.Channels[name]; exists && len(raw) > 0 {
			json.Unmarshal(raw, &tgCh)
		}
		tgCh.Type = "telegram"
		tgCh.DMPolicy = "pairing"
		data, _ := json.Marshal(tgCh)
		cfg.Channels[name] = data
		if err := onboard.SaveFileConfigTo(cfg, path); err != nil {
			return Check{Name: "DM policy", Status: Fail, Message: fmt.Sprintf("invalid value %q; fix failed: %v", val, err)}
		}
		return Check{Name: "DM policy", Status: Fail, Message: fmt.Sprintf("invalid value %q", val), Fixed: true}
	}
	return Check{Name: "DM policy", Status: Fail, Message: fmt.Sprintf("invalid value %q", val)}
}

var validStreaming = map[string]bool{
	"off": true, "partial": true, "progress": true,
}

func checkStreaming(fix bool) Check {
	cfg, path, err := ensureConfig()
	if err != nil {
		return Check{Name: "Streaming", Status: Fail, Message: err.Error()}
	}
	ch := getTelegramChannel(cfg)
	val := ch.Streaming
	if val == "" {
		return Check{Name: "Streaming", Status: Pass, Message: "partial (default)"}
	}
	if validStreaming[val] {
		return Check{Name: "Streaming", Status: Pass, Message: val}
	}
	if fix {
		if cfg.Channels == nil {
			cfg.Channels = make(map[string]json.RawMessage)
		}
		name := getTelegramChannelName(cfg)
		if name == "" {
			name = "telegram"
		}
		var tgCh onboard.TelegramChannelConfig
		if raw, exists := cfg.Channels[name]; exists && len(raw) > 0 {
			json.Unmarshal(raw, &tgCh)
		}
		tgCh.Type = "telegram"
		tgCh.Streaming = "partial"
		data, _ := json.Marshal(tgCh)
		cfg.Channels[name] = data
		if err := onboard.SaveFileConfigTo(cfg, path); err != nil {
			return Check{Name: "Streaming", Status: Fail, Message: fmt.Sprintf("invalid value %q; fix failed: %v", val, err)}
		}
		return Check{Name: "Streaming", Status: Fail, Message: fmt.Sprintf("invalid value %q", val), Fixed: true}
	}
	return Check{Name: "Streaming", Status: Fail, Message: fmt.Sprintf("invalid value %q", val)}
}

var validChunkMode = map[string]bool{
	"length": true, "newline": true,
}

func checkChunkMode(fix bool) Check {
	cfg, path, err := ensureConfig()
	if err != nil {
		return Check{Name: "Chunk mode", Status: Fail, Message: err.Error()}
	}
	ch := getTelegramChannel(cfg)
	val := ch.ChunkMode
	if val == "" {
		return Check{Name: "Chunk mode", Status: Pass, Message: "length (default)"}
	}
	if validChunkMode[val] {
		return Check{Name: "Chunk mode", Status: Pass, Message: val}
	}
	if fix {
		if cfg.Channels == nil {
			cfg.Channels = make(map[string]json.RawMessage)
		}
		name := getTelegramChannelName(cfg)
		if name == "" {
			name = "telegram"
		}
		var tgCh onboard.TelegramChannelConfig
		if raw, exists := cfg.Channels[name]; exists && len(raw) > 0 {
			json.Unmarshal(raw, &tgCh)
		}
		tgCh.Type = "telegram"
		tgCh.ChunkMode = "length"
		data, _ := json.Marshal(tgCh)
		cfg.Channels[name] = data
		if err := onboard.SaveFileConfigTo(cfg, path); err != nil {
			return Check{Name: "Chunk mode", Status: Fail, Message: fmt.Sprintf("invalid value %q; fix failed: %v", val, err)}
		}
		return Check{Name: "Chunk mode", Status: Fail, Message: fmt.Sprintf("invalid value %q", val), Fixed: true}
	}
	return Check{Name: "Chunk mode", Status: Fail, Message: fmt.Sprintf("invalid value %q", val)}
}

func checkStalePID(fix bool) Check {
	pid, err := daemon.ReadPID()
	if err != nil {
		return Check{Name: "Stale PID", Status: Fail, Message: err.Error()}
	}
	if pid == 0 {
		return Check{Name: "Gateway", Status: Pass, Message: "not running (no PID file)"}
	}
	if daemon.IsRunning(pid) {
		return Check{Name: "Gateway", Status: Pass, Message: fmt.Sprintf("running (pid %d)", pid)}
	}
	// Stale PID
	if fix {
		daemon.RemovePID()
		return Check{Name: "Stale PID", Status: Warn, Message: fmt.Sprintf("pid %d is not running", pid), Fixed: true}
	}
	return Check{Name: "Stale PID", Status: Warn, Message: fmt.Sprintf("pid %d is not running (stale PID file)", pid)}
}

func checkGatewayHealth(fix bool) Check {
	cfg, _, err := ensureConfig()
	if err != nil {
		return Check{Name: "Health check", Status: Warn, Message: "cannot load config"}
	}
	addr := cfg.Gateway.Address
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	url := "http://" + addr + "/healthz"

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return Check{Name: "Health check", Status: Warn, Message: "gateway not reachable"}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Check{Name: "Health check", Status: Warn, Message: fmt.Sprintf("/healthz returned %d", resp.StatusCode)}
	}
	return Check{Name: "Health check", Status: Pass, Message: "/healthz OK"}
}

func checkDMPolicyOpen(fix bool) Check {
	cfg, _, err := ensureConfig()
	if err != nil {
		return Check{Name: "Security", Status: Warn, Message: "cannot load config"}
	}
	ch := getTelegramChannel(cfg)
	if ch.DMPolicy == "open" {
		return Check{Name: "Security", Status: Warn, Message: "dm_policy is \"open\" — anyone can message the bot"}
	}
	return Check{Name: "Security", Status: Pass, Message: "dm_policy is not open"}
}

func checkDataDirWritable(fix bool) Check {
	dir, err := daemon.DataDir()
	if err != nil {
		return Check{Name: "Data directory", Status: Fail, Message: err.Error()}
	}
	// Try writing a temp file to verify writability.
	tmp := dir + "/.doctor-probe"
	if err := os.WriteFile(tmp, []byte("ok"), 0o644); err != nil {
		return Check{Name: "Data directory", Status: Fail, Message: fmt.Sprintf("%s is not writable: %v", dir, err)}
	}
	os.Remove(tmp)
	return Check{Name: "Data directory", Status: Pass, Message: dir}
}
