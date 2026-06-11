// Package onboard implements the interactive setup wizard for clawdex.
package onboard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Rememorio/clawdex/internal/codex"
	"github.com/Rememorio/clawdex/internal/daemon"
	"github.com/Rememorio/clawdex/internal/secret"
	"github.com/Rememorio/clawdex/internal/termcolor"
	"github.com/Rememorio/clawdex/internal/version"
)

var term = termcolor.New(os.Stdout)

func bold(s string) string {
	return term.Bold(s)
}

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

const configFileName = "clawdex.json"

var errRefuseEmptyConfigOverwrite = errors.New(
	"refusing to overwrite existing config with empty config",
)

// FileConfig is the JSON-serializable configuration stored on disk.
type FileConfig struct {
	Meta     MetaFileConfig             `json:"meta,omitempty"`
	Codex    CodexFileConfig            `json:"codex"`
	Gateway  GatewayFileConfig          `json:"gateway"`
	Cron     CronFileConfig             `json:"cron,omitempty"`
	Logging  LoggingFileConfig          `json:"logging,omitempty"`
	Channels map[string]json.RawMessage `json:"channels,omitempty"`
}

// MetaFileConfig tracks metadata about the config file.
type MetaFileConfig struct {
	LastTouchedVersion string `json:"lastTouchedVersion,omitempty"`
	LastTouchedAt      string `json:"lastTouchedAt,omitempty"`
}

// LoggingFileConfig holds logging configuration.
type LoggingFileConfig struct {
	Level     string `json:"level,omitempty"`      // debug, info, warn, error
	Format    string `json:"format,omitempty"`     // text, json
	CodexFile string `json:"codex_file,omitempty"` // detailed Codex trace log file
}

// TelegramChannelConfig represents a Telegram channel configuration.
type TelegramChannelConfig struct {
	Type           string                       `json:"type"`
	BotToken       string                       `json:"bot_token,omitempty"`
	Enabled        *bool                        `json:"enabled,omitempty"`
	DMPolicy       string                       `json:"dm_policy,omitempty"`
	ChunkMode      string                       `json:"chunk_mode,omitempty"`
	TextChunkLimit int                          `json:"text_chunk_limit,omitempty"`
	Streaming      string                       `json:"streaming,omitempty"`
	AllowFrom      []int64                      `json:"allow_from"`
	GroupPolicy    string                       `json:"group_policy,omitempty"`
	GroupAllowFrom []int64                      `json:"group_allow_from"`
	Groups         map[string]TelegramGroupRule `json:"groups,omitempty"`
	RequireMention *bool                        `json:"require_mention,omitempty"`
}

// TelegramGroupRule defines per-group access settings for Telegram.
type TelegramGroupRule struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	AllowFrom      []int64 `json:"allow_from,omitempty"`
	RequireMention *bool   `json:"require_mention,omitempty"`
}

// WeComChannelConfig represents a WeCom channel configuration.
type WeComChannelConfig struct {
	Type           string                        `json:"type"`
	Enabled        *bool                         `json:"enabled,omitempty"`
	Token          string                        `json:"token,omitempty"`
	EncodingAESKey string                        `json:"encoding_aes_key,omitempty"`
	WebhookPath    string                        `json:"webhook_path,omitempty"`
	DMPolicy       string                        `json:"dm_policy,omitempty"`
	AllowFrom      []string                      `json:"allow_from"`
	GroupPolicy    string                        `json:"group_policy,omitempty"`
	GroupAllowFrom []string                      `json:"group_allow_from"`
	Groups         map[string]WeComGroupRuleFile `json:"groups,omitempty"`
	ConnectionMode string                        `json:"connection_mode,omitempty"`
	BotID          string                        `json:"botid,omitempty"`
	Secret         string                        `json:"secret,omitempty"`
	WSURL          string                        `json:"ws_url,omitempty"`
	HeartbeatInt   string                        `json:"heartbeat_interval,omitempty"`
	TextChunkLimit int                           `json:"text_chunk_limit,omitempty"`
}

// CodexFileConfig holds Codex-related settings.
type CodexFileConfig struct {
	WorkDir        string `json:"workdir,omitempty"`
	Timeout        string `json:"timeout,omitempty"`
	MaxOutputRunes int    `json:"max_output_runes,omitempty"`
	Sandbox        string `json:"sandbox,omitempty"`
	GroupSandbox   string `json:"group_sandbox,omitempty"`
}

// GatewayFileConfig holds gateway server settings.
type GatewayFileConfig struct {
	Address string `json:"address,omitempty"`
}

// CronFileConfig holds scheduled job configuration.
type CronFileConfig struct {
	Enabled    *bool  `json:"enabled,omitempty"`
	Store      string `json:"store,omitempty"`
	MCPEnabled *bool  `json:"mcp_enabled,omitempty"`
}

// WeComGroupRuleFile defines per-group access settings for the config file.
type WeComGroupRuleFile struct {
	Enabled   *bool    `json:"enabled,omitempty"`
	AllowFrom []string `json:"allow_from,omitempty"`
}

// WeixinChannelConfig represents a Weixin (personal WeChat) channel configuration.
type WeixinChannelConfig struct {
	Type           string   `json:"type"`
	Enabled        *bool    `json:"enabled,omitempty"`
	BaseURL        string   `json:"base_url,omitempty"`
	Token          string   `json:"token,omitempty"`
	DMPolicy       string   `json:"dm_policy,omitempty"`
	AllowFrom      []string `json:"allow_from"`
	TextChunkLimit int      `json:"text_chunk_limit,omitempty"`
}

// QQBotChannelConfig represents a QQ Bot channel configuration.
type QQBotChannelConfig struct {
	Type           string   `json:"type"`
	Enabled        *bool    `json:"enabled,omitempty"`
	AppID          string   `json:"app_id,omitempty"`
	ClientSecret   string   `json:"client_secret,omitempty"`
	DMPolicy       string   `json:"dm_policy,omitempty"`
	AllowFrom      []string `json:"allow_from"`
	GroupPolicy    string   `json:"group_policy,omitempty"`
	GroupAllowFrom []string `json:"group_allow_from"`
	TextChunkLimit int      `json:"text_chunk_limit,omitempty"`
}

// FeishuChannelConfig represents a Feishu bot channel configuration.
type FeishuChannelConfig struct {
	Type           string                     `json:"type"`
	Enabled        *bool                      `json:"enabled,omitempty"`
	AppID          string                     `json:"app_id,omitempty"`
	AppSecret      string                     `json:"app_secret,omitempty"`
	BaseURL        string                     `json:"base_url,omitempty"`
	DMPolicy       string                     `json:"dm_policy,omitempty"`
	AllowFrom      []string                   `json:"allow_from"`
	GroupPolicy    string                     `json:"group_policy,omitempty"`
	GroupAllowFrom []string                   `json:"group_allow_from"`
	Groups         map[string]FeishuGroupRule `json:"groups,omitempty"`
	RequireMention *bool                      `json:"require_mention,omitempty"`
	TextChunkLimit int                        `json:"text_chunk_limit,omitempty"`
}

// FeishuGroupRule defines per-group access settings for the config file.
type FeishuGroupRule struct {
	Enabled        *bool    `json:"enabled,omitempty"`
	AllowFrom      []string `json:"allow_from,omitempty"`
	RequireMention *bool    `json:"require_mention,omitempty"`
}

// channelTypeMeta is used to extract the type field from raw JSON.
type channelTypeMeta struct {
	Type string `json:"type"`
}

// ParseChannelConfig parses a raw JSON message into the appropriate channel config type.
func ParseChannelConfig(raw json.RawMessage) (any, error) {
	var meta channelTypeMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("parse channel type: %w", err)
	}
	switch meta.Type {
	case "telegram":
		var cfg TelegramChannelConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse telegram config: %w", err)
		}
		return cfg, nil
	case "wecom":
		var cfg WeComChannelConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse wecom config: %w", err)
		}
		return cfg, nil
	case "weixin":
		var cfg WeixinChannelConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse weixin config: %w", err)
		}
		return cfg, nil
	case "qqbot":
		var cfg QQBotChannelConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse qqbot config: %w", err)
		}
		return cfg, nil
	case "feishu":
		var cfg FeishuChannelConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("parse feishu config: %w", err)
		}
		return cfg, nil
	default:
		return nil, fmt.Errorf("unknown channel type: %s", meta.Type)
	}
}

// ParseChannelConfigs parses all raw channel configs into typed configs.
func ParseChannelConfigs(raw map[string]json.RawMessage) (map[string]any, error) {
	result := make(map[string]any, len(raw))
	for name, data := range raw {
		cfg, err := ParseChannelConfig(data)
		if err != nil {
			return nil, fmt.Errorf("channel %q: %w", name, err)
		}
		result[name] = cfg
	}
	return result, nil
}

// ChannelType returns the type of a channel config.
func ChannelType(raw json.RawMessage) (string, error) {
	var meta channelTypeMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "", fmt.Errorf("parse channel type: %w", err)
	}
	return meta.Type, nil
}

// MarshalTelegramChannel marshals a TelegramChannelConfig to json.RawMessage.
func MarshalTelegramChannel(cfg TelegramChannelConfig) json.RawMessage {
	data, _ := json.Marshal(cfg)
	return data
}

// MarshalWeComChannel marshals a WeComChannelConfig to json.RawMessage.
func MarshalWeComChannel(cfg WeComChannelConfig) json.RawMessage {
	data, _ := json.Marshal(cfg)
	return data
}

// MarshalWeixinChannel marshals a WeixinChannelConfig to json.RawMessage.
func MarshalWeixinChannel(cfg WeixinChannelConfig) json.RawMessage {
	data, _ := json.Marshal(cfg)
	return data
}

// MarshalQQBotChannel marshals a QQBotChannelConfig to json.RawMessage.
func MarshalQQBotChannel(cfg QQBotChannelConfig) json.RawMessage {
	data, _ := json.Marshal(cfg)
	return data
}

// MarshalFeishuChannel marshals a FeishuChannelConfig to json.RawMessage.
func MarshalFeishuChannel(cfg FeishuChannelConfig) json.RawMessage {
	data, _ := json.Marshal(cfg)
	return data
}

// MustParseTelegramChannel parses raw JSON as TelegramChannelConfig, panicking on error.
func MustParseTelegramChannel(raw json.RawMessage) TelegramChannelConfig {
	var cfg TelegramChannelConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		panic(err)
	}
	return cfg
}

// MustParseWeComChannel parses raw JSON as WeComChannelConfig, panicking on error.
func MustParseWeComChannel(raw json.RawMessage) WeComChannelConfig {
	var cfg WeComChannelConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		panic(err)
	}
	return cfg
}

// ConfigPath returns the full path to the config file.
func ConfigPath() (string, error) {
	dir, err := daemon.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

// SoulPath returns the full path to the SOUL.md file (~/.clawdex/SOUL.md).
func SoulPath() (string, error) {
	dir, err := daemon.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "SOUL.md"), nil
}

// InstanceSoulPath returns the path to a per-instance SOUL file:
// ~/.clawdex/SOUL-<name>.md. Returns "" if name is empty.
func InstanceSoulPath(name string) (string, error) {
	if name == "" {
		return "", nil
	}
	dir, err := daemon.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "SOUL-"+name+".md"), nil
}

// LoadFileConfig reads the config file from disk. Returns a zero-value config
// if the file does not exist.
func LoadFileConfig() (*FileConfig, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadFileConfigFrom(path)
}

// LoadFileConfigFrom reads the config file from the given path.
func LoadFileConfigFrom(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &FileConfig{}, nil
		}
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var cfg FileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	return &cfg, nil
}

// SaveFileConfig writes the config to disk as formatted JSON.
// It automatically updates meta fields before saving.
func SaveFileConfig(cfg *FileConfig) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	return SaveFileConfigTo(cfg, path)
}

// SaveFileConfigTo writes the config to the given path as formatted JSON.
// It uses atomic write (temp file + rename) to prevent data loss if the
// process is interrupted mid-write. Meta fields are updated automatically.
func SaveFileConfigTo(cfg *FileConfig, path string) error {
	if err := refuseEmptyConfigOverwrite(cfg, path); err != nil {
		return err
	}

	// Normalize channel defaults before saving.
	if err := applyChannelDefaults(cfg); err != nil {
		return err
	}

	// Update meta fields before saving.
	cfg.Meta = MetaFileConfig{
		LastTouchedVersion: version.Version,
		LastTouchedAt:      time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".clawdex-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename config file: %w", err)
	}
	return nil
}

func refuseEmptyConfigOverwrite(cfg *FileConfig, path string) error {
	if hasMeaningfulConfig(cfg) {
		return nil
	}

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat config file: %w", err)
	}

	existing, err := LoadFileConfigFrom(path)
	if err != nil {
		return err
	}
	if !hasMeaningfulConfig(existing) {
		return nil
	}
	return errRefuseEmptyConfigOverwrite
}

func hasMeaningfulConfig(cfg *FileConfig) bool {
	if cfg == nil {
		return false
	}
	if cfg.Codex.WorkDir != "" || cfg.Codex.Timeout != "" ||
		cfg.Codex.MaxOutputRunes != 0 || cfg.Codex.Sandbox != "" ||
		cfg.Codex.GroupSandbox != "" {
		return true
	}
	if cfg.Gateway.Address != "" {
		return true
	}
	if cfg.Cron.Enabled != nil || cfg.Cron.Store != "" || cfg.Cron.MCPEnabled != nil {
		return true
	}
	if cfg.Logging.Level != "" || cfg.Logging.Format != "" ||
		cfg.Logging.CodexFile != "" {
		return true
	}
	return len(cfg.Channels) > 0
}

func applyChannelDefaults(cfg *FileConfig) error {
	if cfg == nil || len(cfg.Channels) == 0 {
		return nil
	}

	for name, raw := range cfg.Channels {
		chType, err := ChannelType(raw)
		if err != nil {
			return fmt.Errorf("channel %q: parse channel type: %w", name, err)
		}

		switch chType {
		case "telegram":
			var ch TelegramChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				return fmt.Errorf("channel %q: parse telegram config: %w", name, err)
			}
			applyTelegramDefaults(&ch)
			cfg.Channels[name] = MarshalTelegramChannel(ch)
		case "wecom":
			var ch WeComChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				return fmt.Errorf("channel %q: parse wecom config: %w", name, err)
			}
			applyWeComDefaults(&ch)
			cfg.Channels[name] = MarshalWeComChannel(ch)
		case "weixin":
			var ch WeixinChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				return fmt.Errorf("channel %q: parse weixin config: %w", name, err)
			}
			applyWeixinDefaults(&ch)
			cfg.Channels[name] = MarshalWeixinChannel(ch)
		case "feishu":
			var ch FeishuChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				return fmt.Errorf("channel %q: parse feishu config: %w", name, err)
			}
			applyFeishuDefaults(&ch)
			cfg.Channels[name] = MarshalFeishuChannel(ch)
		}
	}
	return nil
}

func applyTelegramDefaults(ch *TelegramChannelConfig) {
	if ch == nil {
		return
	}
	if ch.Type == "" {
		ch.Type = "telegram"
	}
	if ch.Enabled == nil {
		enabled := true
		ch.Enabled = &enabled
	}
	if ch.DMPolicy == "" {
		ch.DMPolicy = "pairing"
	}
	if ch.ChunkMode == "" {
		ch.ChunkMode = "length"
	}
	if ch.TextChunkLimit == 0 {
		ch.TextChunkLimit = 3500
	}
	if ch.Streaming == "" {
		ch.Streaming = "partial"
	}
	if ch.AllowFrom == nil {
		ch.AllowFrom = []int64{}
	}
	if ch.GroupPolicy == "" {
		ch.GroupPolicy = "allowlist"
	}
	if ch.GroupAllowFrom == nil {
		ch.GroupAllowFrom = []int64{}
	}
	if ch.RequireMention == nil {
		requireMention := true
		ch.RequireMention = &requireMention
	}
}

func applyWeComDefaults(ch *WeComChannelConfig) {
	if ch == nil {
		return
	}
	if ch.Type == "" {
		ch.Type = "wecom"
	}
	if ch.DMPolicy == "" {
		ch.DMPolicy = "pairing"
	}
	if ch.AllowFrom == nil {
		ch.AllowFrom = []string{}
	}
	if ch.GroupPolicy == "" {
		ch.GroupPolicy = "allowlist"
	}
	if ch.GroupAllowFrom == nil {
		ch.GroupAllowFrom = []string{}
	}
	if ch.TextChunkLimit == 0 {
		ch.TextChunkLimit = 4096
	}
}

func applyWeixinDefaults(ch *WeixinChannelConfig) {
	if ch == nil {
		return
	}
	if ch.Type == "" {
		ch.Type = "weixin"
	}
	if ch.Enabled == nil {
		enabled := true
		ch.Enabled = &enabled
	}
	if ch.DMPolicy == "" {
		ch.DMPolicy = "open"
	}
	if ch.AllowFrom == nil {
		ch.AllowFrom = []string{}
	}
	if ch.TextChunkLimit == 0 {
		ch.TextChunkLimit = 4000
	}
}

func applyFeishuDefaults(ch *FeishuChannelConfig) {
	if ch == nil {
		return
	}
	if ch.Type == "" {
		ch.Type = "feishu"
	}
	if ch.Enabled == nil {
		enabled := true
		ch.Enabled = &enabled
	}
	if ch.DMPolicy == "" {
		ch.DMPolicy = "pairing"
	}
	if ch.AllowFrom == nil {
		ch.AllowFrom = []string{}
	}
	if ch.GroupPolicy == "" {
		ch.GroupPolicy = "allowlist"
	}
	if ch.GroupAllowFrom == nil {
		ch.GroupAllowFrom = []string{}
	}
	if ch.RequireMention == nil {
		requireMention := true
		ch.RequireMention = &requireMention
	}
	if ch.TextChunkLimit == 0 {
		ch.TextChunkLimit = 4000
	}
}

// RunOption is a functional option for the Run function.
type RunOption func(*runOptions)

type runOptions struct {
	installDaemon bool
}

// WithInstallDaemon configures whether to install the daemon after onboarding.
func WithInstallDaemon(install bool) RunOption {
	return func(o *runOptions) {
		o.installDaemon = install
	}
}

// Run executes the interactive onboarding wizard.
func Run(opts ...RunOption) error {
	return runWithBaseURL("https://api.telegram.org/bot", opts...)
}

// runWithBaseURL is the testable core of Run.
func runWithBaseURL(telegramBaseURL string, opts ...RunOption) error {
	var ro runOptions
	for _, opt := range opts {
		opt(&ro)
	}

	reader := bufio.NewReader(os.Stdin)

	fmt.Println(bold("clawdex onboard"))
	fmt.Println("--------")
	fmt.Println()

	existing, err := LoadFileConfig()
	if err != nil {
		return err
	}

	cfg := *existing

	// ── Section 1: Codex ──
	fmt.Println(bold("Codex CLI"))

	if err := probeCodex(); err != nil {
		return err
	}

	// ── Section 2: Channel configuration (loop) ──
	fmt.Println()
	fmt.Println(bold("Channels"))
	fmt.Println()

	if cfg.Channels == nil {
		cfg.Channels = make(map[string]json.RawMessage)
	}

	if err := configureChannelsLoop(reader, &cfg, telegramBaseURL); err != nil {
		return err
	}

	// Set default Codex configuration skeleton if not already set.
	if cfg.Codex.Sandbox == "" {
		cfg.Codex.Sandbox = "workspace-write"
	}
	if cfg.Codex.Timeout == "" {
		cfg.Codex.Timeout = "120m"
	}
	// GroupSandbox defaults to read-only for safety.
	if cfg.Codex.GroupSandbox == "" {
		cfg.Codex.GroupSandbox = "read-only"
	}

	// Set default Gateway configuration if not already set.
	if cfg.Gateway.Address == "" {
		cfg.Gateway.Address = ":8080"
	}

	// ── Save ──
	if err := SaveFileConfig(&cfg); err != nil {
		return err
	}

	configPath, _ := ConfigPath()
	fmt.Println()
	fmt.Println("--------")
	fmt.Printf("%s Saved to %s\n", green("✓"), configPath)
	fmt.Println()

	// ── Install daemon if requested ──
	if ro.installDaemon {
		fmt.Println("Installing daemon...")
		if err := daemon.Install(); err != nil {
			return fmt.Errorf("install daemon: %w", err)
		}
		fmt.Println()
	}

	// If the gateway is already running, offer to restart it to apply changes.
	pid, _ := daemon.ReadPID()
	if pid > 0 && daemon.IsRunning(pid) {
		answer, err := prompt(reader, "Gateway is running. Restart now? [Y/n]", "Y")
		if err != nil {
			return err
		}
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer == "" || answer == "y" || answer == "yes" {
			fmt.Printf("  Restarting gateway...")
			if err := daemon.Restart(); err != nil {
				fmt.Printf(" %s\n", red("failed: "+err.Error()))
				fmt.Printf("\n  Run manually: %s\n", cyan("clawdex gateway restart"))
			} else {
				fmt.Printf(" %s\n", green("done"))
			}
		} else {
			fmt.Printf("\n  Run %s to apply changes.\n", cyan("clawdex gateway restart"))
		}
	} else {
		fmt.Printf("  %s\n", cyan("clawdex gateway start"))
	}
	return nil
}

// configureChannelsLoop provides a unified loop for configuring all channel types.
// Users can add Telegram or WeCom instances, and exit when done.
func configureChannelsLoop(reader *bufio.Reader, cfg *FileConfig, telegramBaseURL string) error {
	for {
		// Count existing instances by type.
		tgCount := 0
		wcCount := 0
		wxCount := 0
		qqCount := 0
		fsCount := 0
		for _, raw := range cfg.Channels {
			chType, _ := ChannelType(raw)
			switch chType {
			case "telegram":
				tgCount++
			case "wecom":
				wcCount++
			case "weixin":
				wxCount++
			case "qqbot":
				qqCount++
			case "feishu":
				fsCount++
			}
		}

		// Show current status.
		fmt.Println("  Current configuration:")
		if tgCount > 0 {
			fmt.Printf("    • Telegram: %s\n", green(fmt.Sprintf("%d instance(s)", tgCount)))
		}
		if wcCount > 0 {
			fmt.Printf("    • WeCom: %s\n", green(fmt.Sprintf("%d instance(s)", wcCount)))
		}
		if wxCount > 0 {
			fmt.Printf("    • Weixin: %s\n", green(fmt.Sprintf("%d instance(s)", wxCount)))
		}
		if qqCount > 0 {
			fmt.Printf("    • QQ Bot: %s\n", green(fmt.Sprintf("%d instance(s)", qqCount)))
		}
		if fsCount > 0 {
			fmt.Printf("    • Feishu: %s\n", green(fmt.Sprintf("%d instance(s)", fsCount)))
		}
		if tgCount == 0 && wcCount == 0 && wxCount == 0 && qqCount == 0 && fsCount == 0 {
			fmt.Printf("    %s\n", dim("(none)"))
		}

		fmt.Println()
		fmt.Println("  Options:")
		fmt.Println("    > 1. Add Telegram instance")
		fmt.Println("      2. Add WeCom instance")
		fmt.Println("      3. Add Weixin instance")
		fmt.Println("      4. Add QQ Bot instance")
		fmt.Println("      5. Done")
		fmt.Println("      6. Add Feishu instance")
		fmt.Println()

		choice, err := promptChoice(reader, "Choice [1/2/3/4/5/6]", "1", map[string]bool{
			"1": true, "2": true, "3": true, "4": true, "5": true, "6": true,
		})
		if err != nil {
			return err
		}

		switch choice {
		case "1":
			if err := addTelegramInstance(reader, cfg, telegramBaseURL); err != nil {
				return err
			}
		case "2":
			if err := addWeComInstance(reader, cfg); err != nil {
				return err
			}
		case "3":
			if err := addWeixinInstance(reader, cfg); err != nil {
				return err
			}
		case "4":
			if err := addQQBotInstance(reader, cfg); err != nil {
				return err
			}
		case "5":
			return nil
		case "6":
			if err := addFeishuInstance(reader, cfg); err != nil {
				return err
			}
		}

		fmt.Println()
	}
}

// addTelegramInstance adds a single Telegram instance interactively.
func addTelegramInstance(reader *bufio.Reader, cfg *FileConfig, telegramBaseURL string) error {
	fmt.Println()
	fmt.Println(bold("Telegram"))

	// Count existing telegram instances.
	tgCount := 0
	for _, raw := range cfg.Channels {
		chType, _ := ChannelType(raw)
		if chType == "telegram" {
			tgCount++
		}
	}

	// Determine default name.
	defaultName := "telegram"
	if tgCount > 0 {
		defaultName = fmt.Sprintf("telegram-%d", tgCount+1)
	}

	name, err := prompt(reader, "Instance name", defaultName)
	if err != nil {
		return err
	}

	ch, err := setupTelegramChannelInstance(reader, cfg, name, telegramBaseURL)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(ch)
	cfg.Channels[name] = data
	return nil
}

// setupTelegramChannelInstance configures a single Telegram instance interactively.
func setupTelegramChannelInstance(reader *bufio.Reader, cfg *FileConfig, name, telegramBaseURL string) (TelegramChannelConfig, error) {
	ch := TelegramChannelConfig{Type: "telegram"}

	fmt.Println()

	// Get current config from channels map.
	currentToken := ""
	if raw, exists := cfg.Channels[name]; exists {
		var existing TelegramChannelConfig
		if err := json.Unmarshal(raw, &existing); err == nil {
			currentToken = existing.BotToken
		}
	}

	tokenRef, err := promptBotToken(reader, currentToken)
	if err != nil {
		return ch, err
	}

	// Resolve the token and validate it against the Telegram API.
	resolvedToken, err := secret.Resolve(tokenRef)
	if err != nil {
		return ch, fmt.Errorf("resolve bot token: %w", err)
	}
	if resolvedToken == "" {
		return ch, fmt.Errorf("bot token resolved to empty value")
	}

	botUser, err := verifyBotTokenWithURL(telegramBaseURL, resolvedToken)
	if err != nil {
		return ch, err
	}

	// Set default configuration.
	t := true
	ch.BotToken = tokenRef
	ch.Enabled = &t
	ch.DMPolicy = "pairing"
	ch.Streaming = "partial"
	ch.ChunkMode = "length"
	ch.TextChunkLimit = 3500
	ch.AllowFrom = []int64{}
	ch.GroupPolicy = "allowlist"
	ch.GroupAllowFrom = []int64{}

	// Preserve existing settings if they exist.
	if raw, exists := cfg.Channels[name]; exists {
		var existing TelegramChannelConfig
		if err := json.Unmarshal(raw, &existing); err == nil {
			if existing.DMPolicy != "" {
				ch.DMPolicy = existing.DMPolicy
			}
			if existing.Streaming != "" {
				ch.Streaming = existing.Streaming
			}
			if existing.ChunkMode != "" {
				ch.ChunkMode = existing.ChunkMode
			}
			if existing.TextChunkLimit > 0 {
				ch.TextChunkLimit = existing.TextChunkLimit
			}
			if existing.AllowFrom != nil {
				ch.AllowFrom = existing.AllowFrom
			}
			if existing.GroupPolicy != "" {
				ch.GroupPolicy = existing.GroupPolicy
			}
			if existing.GroupAllowFrom != nil {
				ch.GroupAllowFrom = existing.GroupAllowFrom
			}
			if existing.Groups != nil {
				ch.Groups = existing.Groups
			}
			if existing.RequireMention != nil {
				ch.RequireMention = existing.RequireMention
			}
		}
	}

	fmt.Printf("  %s Bot verified: @%s (id: %d)\n", green("✓"), botUser.Username, botUser.ID)
	fmt.Println()
	fmt.Printf("  %s DM policy defaults to %s — unknown users get a pairing code\n", dim("ℹ"), bold("pairing"))

	return ch, nil
}

// addWeComInstance adds a single WeCom instance interactively.
func addWeComInstance(reader *bufio.Reader, cfg *FileConfig) error {
	fmt.Println()
	fmt.Println(bold("WeCom (企业微信)"))

	// Count existing wecom instances.
	wcCount := 0
	for _, raw := range cfg.Channels {
		chType, _ := ChannelType(raw)
		if chType == "wecom" {
			wcCount++
		}
	}

	// Determine default name.
	defaultName := "wecom"
	if wcCount > 0 {
		defaultName = fmt.Sprintf("wecom-%d", wcCount+1)
	}

	name, err := prompt(reader, "Instance name", defaultName)
	if err != nil {
		return err
	}

	ch, err := setupWeComChannelInstance(reader, name)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(ch)
	cfg.Channels[name] = data
	return nil
}

// addWeixinInstance adds a single Weixin instance interactively.
func addWeixinInstance(reader *bufio.Reader, cfg *FileConfig) error {
	fmt.Println()
	fmt.Println(bold("Weixin (微信)"))

	// Count existing weixin instances.
	wxCount := 0
	for _, raw := range cfg.Channels {
		chType, _ := ChannelType(raw)
		if chType == "weixin" {
			wxCount++
		}
	}

	// Determine default name.
	defaultName := "weixin"
	if wxCount > 0 {
		defaultName = fmt.Sprintf("weixin-%d", wxCount+1)
	}

	name, err := prompt(reader, "Instance name", defaultName)
	if err != nil {
		return err
	}

	ch, err := setupWeixinChannelInstance(reader, name)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(ch)
	cfg.Channels[name] = data
	return nil
}

// setupWeixinChannelInstance configures a single Weixin instance via QR login.
func setupWeixinChannelInstance(_ *bufio.Reader, name string) (WeixinChannelConfig, error) {
	ch := WeixinChannelConfig{Type: "weixin"}

	fmt.Println()
	fmt.Printf("  %s\n", dim("Scan the QR code with your WeChat app to connect"))
	fmt.Println()

	result, err := weixinQRLogin()
	if err != nil {
		return ch, fmt.Errorf("weixin login: %w", err)
	}

	t := true
	ch.Enabled = &t
	ch.Token = result.Token
	ch.DMPolicy = "open"
	ch.TextChunkLimit = 4000
	// Auto-allow the user who scanned the QR code.
	if result.UserID != "" {
		ch.AllowFrom = []string{result.UserID}
	} else {
		ch.AllowFrom = []string{}
	}
	if result.BaseURL != "" {
		ch.BaseURL = result.BaseURL
	}

	fmt.Printf("  %s Weixin %q connected\n", green("✓"), name)
	fmt.Println()
	fmt.Printf("  %s DM policy defaults to %s — unknown users get a pairing code\n", dim("ℹ"), bold("pairing"))

	return ch, nil
}

// addQQBotInstance adds a single QQ Bot instance interactively.
func addQQBotInstance(reader *bufio.Reader, cfg *FileConfig) error {
	fmt.Println()
	fmt.Println(bold("QQ Bot"))

	// Count existing qqbot instances.
	qqCount := 0
	for _, raw := range cfg.Channels {
		chType, _ := ChannelType(raw)
		if chType == "qqbot" {
			qqCount++
		}
	}

	defaultName := "qqbot"
	if qqCount > 0 {
		defaultName = fmt.Sprintf("qqbot-%d", qqCount+1)
	}

	name, err := prompt(reader, "Instance name", defaultName)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("  %s\n", dim("Get your AppID and ClientSecret from https://q.qq.com"))
	fmt.Println()

	appID, err := prompt(reader, "App ID", "")
	if err != nil {
		return err
	}
	if appID == "" {
		return fmt.Errorf("app_id is required")
	}

	clientSecret, err := prompt(reader, "Client Secret", "")
	if err != nil {
		return err
	}
	if clientSecret == "" {
		return fmt.Errorf("client_secret is required")
	}

	t := true
	ch := QQBotChannelConfig{
		Type:           "qqbot",
		Enabled:        &t,
		AppID:          appID,
		ClientSecret:   clientSecret,
		DMPolicy:       "open",
		GroupPolicy:    "open",
		AllowFrom:      []string{},
		GroupAllowFrom: []string{},
	}

	data, _ := json.Marshal(ch)
	cfg.Channels[name] = data

	fmt.Println()
	fmt.Printf("  %s QQ Bot %q configured\n", green("✓"), name)
	fmt.Printf("  %s DM policy set to %s, group policy set to %s\n", dim("ℹ"), bold("open"), bold("open"))

	return nil
}

// addFeishuInstance adds a single Feishu bot instance interactively.
func addFeishuInstance(reader *bufio.Reader, cfg *FileConfig) error {
	fmt.Println()
	fmt.Println(bold("Feishu"))

	fsCount := 0
	for _, raw := range cfg.Channels {
		chType, _ := ChannelType(raw)
		if chType == "feishu" {
			fsCount++
		}
	}

	defaultName := "feishu"
	if fsCount > 0 {
		defaultName = fmt.Sprintf("feishu-%d", fsCount+1)
	}

	name, err := prompt(reader, "Instance name", defaultName)
	if err != nil {
		return err
	}

	fmt.Println()
	fmt.Printf("  %s\n", dim("Get App ID and App Secret from Feishu Open Platform → Credentials & Basic Info"))
	fmt.Printf("  %s\n", dim("Events & Callbacks should use long connection and subscribe to im.message.receive_v1"))
	fmt.Println()

	appID, err := promptSecret(reader, "App ID", "FEISHU_APP_ID", "")
	if err != nil {
		return err
	}
	if appID == "" {
		return fmt.Errorf("app_id is required")
	}

	appSecret, err := promptSecret(reader, "App Secret", "FEISHU_APP_SECRET", "")
	if err != nil {
		return err
	}
	if appSecret == "" {
		return fmt.Errorf("app_secret is required")
	}

	t := true
	requireMention := true
	ch := FeishuChannelConfig{
		Type:           "feishu",
		Enabled:        &t,
		AppID:          appID,
		AppSecret:      appSecret,
		DMPolicy:       "pairing",
		GroupPolicy:    "allowlist",
		AllowFrom:      []string{},
		GroupAllowFrom: []string{},
		RequireMention: &requireMention,
		TextChunkLimit: 4000,
	}

	data, _ := json.Marshal(ch)
	cfg.Channels[name] = data

	fmt.Println()
	fmt.Printf("  %s Feishu %q configured (long connection)\n", green("✓"), name)
	fmt.Printf("  %s No callback URL needed — bot connects outbound via WebSocket\n", dim("ℹ"))
	fmt.Printf("  %s DM policy defaults to %s; groups require @bot and an allowlist by default\n", dim("ℹ"), bold("pairing"))

	return nil
}

// weixinQRLogin performs the QR code login flow inline during onboarding.
func weixinQRLogin() (*weixinLoginResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	return doWeixinLogin(ctx)
}

// weixinLoginResult mirrors weixin.LoginResult to avoid import cycle with channel/weixin.
type weixinLoginResult struct {
	Token   string
	BotID   string
	BaseURL string
	UserID  string
}

const (
	weixinQRBaseURL   = "https://ilinkai.weixin.qq.com"
	weixinDefaultBase = "https://oai.ilink.bot"
	weixinBotType     = "3"
	weixinPollTimeout = 35 * time.Second
)

// doWeixinLogin fetches a QR code, displays it, and polls until scan completes.
func doWeixinLogin(ctx context.Context) (*weixinLoginResult, error) {
	// Step 1: Get QR code.
	endpoint := weixinQRBaseURL + "/ilink/bot/get_bot_qrcode?bot_type=" + url.QueryEscape(weixinBotType)
	body := []byte(`{"local_token_list":[]}`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request QR code: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("QR code request failed (HTTP %d)", resp.StatusCode)
	}

	var qr struct {
		QRCode           string `json:"qrcode"`
		QRCodeImgContent string `json:"qrcode_img_content"`
	}
	if err := json.Unmarshal(respBody, &qr); err != nil {
		return nil, fmt.Errorf("parse QR response: %w", err)
	}

	qrURL := qr.QRCodeImgContent
	if qrURL == "" {
		qrURL = qr.QRCode
	}
	if qrURL == "" {
		return nil, errors.New("server returned empty QR code")
	}

	// Display QR code URL (user opens in browser or scans from terminal).
	fmt.Printf("  %s\n", bold("请用手机微信扫描以下链接中的二维码："))
	fmt.Println()
	fmt.Printf("  %s\n", qrURL)
	fmt.Println()
	fmt.Printf("  %s\n", dim("等待扫码..."))

	// Step 2: Poll for status.
	currentBase := weixinQRBaseURL
	qrToken := qr.QRCode

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("login timed out")
		default:
		}

		status, err := pollWeixinQRStatus(ctx, currentBase, qrToken)
		if err != nil {
			return nil, err
		}

		switch status.Status {
		case "confirmed":
			if status.BotToken == "" {
				return nil, errors.New("login confirmed but no token received")
			}
			base := status.BaseURL
			if base == "" {
				base = weixinDefaultBase
			}
			return &weixinLoginResult{
				Token:   status.BotToken,
				BotID:   status.IlinkBotID,
				BaseURL: base,
				UserID:  status.IlinkUserID,
			}, nil

		case "scaned":
			fmt.Printf("\r  %s\n", dim("已扫码，等待确认..."))

		case "scaned_but_redirect":
			if status.RedirectHost != "" {
				currentBase = "https://" + status.RedirectHost
			}

		case "binded_redirect":
			return nil, errors.New("this WeChat is already bound to another instance")

		case "expired":
			return nil, errors.New("QR code expired, please re-run onboard")

		case "wait":
			// Continue.
		}
	}
}

func pollWeixinQRStatus(ctx context.Context, baseURL, qrcode string) (*weixinQRStatus, error) {
	endpoint := baseURL + "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	pollClient := &http.Client{Timeout: weixinPollTimeout + 5*time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := pollClient.Do(req)
	if err != nil {
		// Timeout → treat as "wait".
		return &weixinQRStatus{Status: "wait"}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return &weixinQRStatus{Status: "wait"}, nil
	}

	var status weixinQRStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("parse QR status: %w", err)
	}
	return &status, nil
}

type weixinQRStatus struct {
	Status       string `json:"status"`
	BotToken     string `json:"bot_token,omitempty"`
	IlinkBotID   string `json:"ilink_bot_id,omitempty"`
	BaseURL      string `json:"baseurl,omitempty"`
	IlinkUserID  string `json:"ilink_user_id,omitempty"`
	RedirectHost string `json:"redirect_host,omitempty"`
}

// setupWeComChannelInstance configures a single WeCom instance interactively.
func setupWeComChannelInstance(reader *bufio.Reader, name string) (WeComChannelConfig, error) {
	ch := WeComChannelConfig{Type: "wecom"}

	fmt.Println()

	// ── Connection mode ──
	fmt.Println("  Connection mode:")
	fmt.Println("    > 1. Webhook  " + dim("(XML callback, requires Token + EncodingAESKey)"))
	fmt.Println("      2. WebSocket" + dim("(long connection, requires BotID + Secret)"))
	fmt.Println()

	modeChoice, err := promptChoice(reader, "Choice [1/2]", "1", map[string]bool{"1": true, "2": true})
	if err != nil {
		return ch, err
	}

	switch modeChoice {
	case "2":
		ch.ConnectionMode = "websocket"
	default:
		ch.ConnectionMode = "webhook"
	}

	if ch.ConnectionMode == "websocket" {
		fmt.Printf("  %s\n", dim("Create an AI bot in WeCom admin → get BotID and Secret"))
		fmt.Println()

		botIDRef, err := promptSecret(reader, "BotID", "WECOM_BOTID", "")
		if err != nil {
			return ch, err
		}
		ch.BotID = botIDRef

		secretRef, err := promptSecret(reader, "Secret", "WECOM_SECRET", "")
		if err != nil {
			return ch, err
		}
		ch.Secret = secretRef
	} else {
		fmt.Printf("  %s\n", dim("Create a group bot in WeCom admin → get Token and EncodingAESKey"))
		fmt.Println()

		tokenRef, err := promptSecret(reader, "Token", "WECOM_TOKEN", "")
		if err != nil {
			return ch, err
		}
		ch.Token = tokenRef

		aesKeyRef, err := promptSecret(reader, "EncodingAESKey", "WECOM_ENCODING_AES_KEY", "")
		if err != nil {
			return ch, err
		}
		ch.EncodingAESKey = aesKeyRef

		webhookPathRef, err := promptSecret(reader, "Webhook path", "WECOM_WEBHOOK_PATH", "")
		if err != nil {
			return ch, err
		}
		ch.WebhookPath = webhookPathRef
	}

	// Enable WeCom with pairing as default policy.
	t := true
	ch.Enabled = &t
	ch.DMPolicy = "pairing"
	ch.GroupPolicy = "allowlist"
	ch.TextChunkLimit = 4096
	ch.AllowFrom = []string{}
	ch.GroupAllowFrom = []string{}

	fmt.Printf("  %s WeCom %q configured (%s mode)\n", green("✓"), name, ch.ConnectionMode)
	fmt.Println()
	if ch.ConnectionMode == "websocket" {
		fmt.Printf("  %s No callback URL needed — bot connects outbound via WebSocket\n", dim("ℹ"))
	} else {
		fmt.Printf("  %s Set callback URL to %s on your server\n", dim("ℹ"), bold("https://<host>:8080/<webhook_path>"))
	}
	fmt.Printf("  %s DM policy defaults to %s — unknown users get a pairing code\n", dim("ℹ"), bold("pairing"))

	return ch, nil
}

// promptSecret is a generic secret prompt supporting env var, plaintext, file, and keep-current.
func promptSecret(reader *bufio.Reader, label, defaultEnvVar, current string) (string, error) {
	if current != "" {
		fmt.Printf("  Current %s: %s\n", label, secret.Describe(current))
	}

	validChoices := map[string]bool{"1": true, "2": true, "3": true}
	if current != "" {
		validChoices["4"] = true
	}

	for {
		fmt.Printf("  %s source:\n", label)
		fmt.Println("    > 1. Environment variable " + dim("(recommended)"))
		fmt.Println("      2. Plaintext")
		fmt.Println("      3. File path")
		if current != "" {
			fmt.Println("      4. Keep current")
		}
		fmt.Println()

		choice, err := promptChoice(reader, "Choice [1/2/3"+keepOption(current)+"]", "1", validChoices)
		if err != nil {
			return "", err
		}

		switch choice {
		case "1":
			envVar, err := prompt(reader, "Environment variable name", defaultEnvVar)
			if err != nil {
				return "", err
			}
			return "${" + envVar + "}", nil

		case "2":
			val, err := prompt(reader, label, "")
			if err != nil {
				return "", err
			}
			if val == "" {
				fmt.Printf("  %s %s cannot be empty\n", red("✗"), label)
				continue
			}
			return val, nil

		case "3":
			path, err := prompt(reader, "Path to file", "")
			if err != nil {
				return "", err
			}
			if path == "" {
				fmt.Printf("  %s file path cannot be empty\n", red("✗"))
				continue
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				fmt.Printf("  %s cannot resolve path: %s\n", red("✗"), path)
				continue
			}
			return "file://" + abs, nil

		case "4":
			if current != "" {
				return current, nil
			}
			fmt.Printf("  %s no current value to keep\n", red("✗"))
			continue
		}
	}
}

// ── Codex probe ──

// probeCodex verifies that the codex CLI is installed and reachable.
func probeCodex() error {
	cmd := exec.Command("codex", "--version")
	cmd.Env = codex.CleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("  %s codex not found\n", red("✗"))
		return fmt.Errorf("codex CLI not found or not working: %w\n  Please install codex first: https://github.com/openai/codex", err)
	}
	version := strings.TrimSpace(string(out))
	fmt.Printf("  %s %s\n", green("✓"), version)
	return nil
}

// ── Telegram verification ──

// botInfo holds the result from Telegram getMe.
type botInfo struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// verifyBotTokenWithURL calls the Telegram getMe API to validate the bot token.
func verifyBotTokenWithURL(baseURL, token string) (*botInfo, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(baseURL + token + "/getMe")
	if err != nil {
		fmt.Printf("  %s token verification failed\n", red("✗"))
		return nil, fmt.Errorf("telegram API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("  %s token verification failed\n", red("✗"))
		return nil, fmt.Errorf("read telegram response: %w", err)
	}

	var result struct {
		OK          bool    `json:"ok"`
		Description string  `json:"description,omitempty"`
		Result      botInfo `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Printf("  %s token verification failed\n", red("✗"))
		return nil, fmt.Errorf("parse telegram response: %w", err)
	}
	if !result.OK {
		fmt.Printf("  %s token rejected\n", red("✗"))
		desc := result.Description
		if desc == "" {
			desc = "unknown error"
		}
		return nil, fmt.Errorf("telegram rejected the token: %s", desc)
	}

	return &result.Result, nil
}

// ── Token prompt ──

func promptBotToken(reader *bufio.Reader, current string) (string, error) {
	fmt.Printf("  %s\n", dim("How to get a token: chat @BotFather → /newbot → copy token"))
	fmt.Println()

	if current != "" {
		fmt.Printf("  Current: %s\n", secret.Describe(current))
		fmt.Println()
	}

	validChoices := map[string]bool{"1": true, "2": true, "3": true}
	if current != "" {
		validChoices["4"] = true
	}

	for {
		fmt.Println("  Token source:")
		fmt.Println("    > 1. Environment variable " + dim("(recommended)"))
		fmt.Println("      2. Plaintext")
		fmt.Println("      3. File path")
		if current != "" {
			fmt.Println("      4. Keep current")
		}
		fmt.Println()

		choice, err := promptChoice(reader, "Choice [1/2/3"+keepOption(current)+"]", "1", validChoices)
		if err != nil {
			return "", err
		}

		switch choice {
		case "1":
			envVar, err := prompt(reader, "Environment variable name", "TELEGRAM_BOT_TOKEN")
			if err != nil {
				return "", err
			}
			return "${" + envVar + "}", nil

		case "2":
			token, err := prompt(reader, "Telegram bot token", "")
			if err != nil {
				return "", err
			}
			if token == "" {
				fmt.Printf("  %s token cannot be empty\n", red("✗"))
				continue
			}
			return token, nil

		case "3":
			path, err := prompt(reader, "Path to token file", "")
			if err != nil {
				return "", err
			}
			if path == "" {
				fmt.Printf("  %s file path cannot be empty\n", red("✗"))
				continue
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				fmt.Printf("  %s cannot resolve path: %s\n", red("✗"), path)
				continue
			}
			return "file://" + abs, nil

		case "4":
			if current != "" {
				return current, nil
			}
			fmt.Printf("  %s no current value to keep\n", red("✗"))
			continue
		}
	}
}

// ── Helpers ──

// prompt asks the user for input with an optional default value.
func prompt(reader *bufio.Reader, label, defaultVal string) (string, error) {
	if defaultVal != "" {
		fmt.Printf("  %s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("  %s: ", label)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal, nil
	}
	return line, nil
}

// promptChoice asks for a choice and loops until a valid option is selected.
// validChoices is a set of valid choices (e.g., map[string]bool{"1": true, "2": true}).
func promptChoice(reader *bufio.Reader, label, defaultVal string, validChoices map[string]bool) (string, error) {
	for {
		choice, err := prompt(reader, label, defaultVal)
		if err != nil {
			return "", err
		}
		if validChoices[choice] {
			return choice, nil
		}
		fmt.Printf("  %s invalid choice: %s\n", red("✗"), choice)
	}
}

func keepOption(current string) string {
	if current != "" {
		return "/4"
	}
	return ""
}
