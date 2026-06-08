// Package config loads and validates runtime settings from the config file
// and environment variables. Environment variables always take precedence.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Rememorio/clawdex/internal/daemon"
	"github.com/Rememorio/clawdex/internal/onboard"
	"github.com/Rememorio/clawdex/internal/secret"
)

// Config is the root runtime configuration for the gateway process.
type Config struct {
	Server   ServerConfig
	Logging  LoggingConfig
	Telegram []TelegramConfig
	WeCom    []WeComConfig
	Weixin   []WeixinConfig
	QQBot    []QQBotConfig
	Feishu   []FeishuConfig
	Codex    CodexConfig
}

// ServerConfig controls the HTTP gateway server.
type ServerConfig struct {
	Address string
}

// LoggingConfig controls logging behavior.
type LoggingConfig struct {
	Level     string // debug, info, warn, error
	Format    string // text, json
	CodexFile string // detailed Codex trace log file
}

// TelegramConfig controls Telegram channel integration.
type TelegramConfig struct {
	Name                string
	BotToken            string
	Enabled             bool
	DMPolicy            string // "open", "pairing" (default), "allowlist"
	PollTimeout         int
	StartupProbeTimeout time.Duration
	ChunkMode           string
	TextChunkLimit      int
	Streaming           string
	AllowFrom           []int64
	SoulContent         string                      // per-instance SOUL content (from SOUL-<name>.md)
	SoulPath            string                      // path to SOUL-<name>.md for dynamic reloads
	SoulOverride        bool                        // true when SOUL-<name>.md was loaded at startup
	GroupPolicy         string                      // "allowlist" (default), "open", "disabled"
	GroupAllowFrom      []int64                     // group-level chatID allowlist
	Groups              map[int64]TelegramGroupRule // chatID → rule; -1 = wildcard fallback
	RequireMention      *bool                       // global default for requireMention in groups
}

// TelegramGroupRule defines per-group access settings for Telegram.
type TelegramGroupRule struct {
	Enabled        *bool
	AllowFrom      []int64
	RequireMention *bool
}

// CodexConfig controls Codex CLI invocation behavior.
type CodexConfig struct {
	WorkDir        string
	CommandTimeout time.Duration
	Sandbox        string
	GroupSandbox   string
	SoulContent    string // contents of SOUL.md, injected via -c instructions
	SoulPath       string // path to SOUL.md for dynamic reloads
}

// WeComGroupRule defines per-group access settings.
type WeComGroupRule struct {
	Enabled   *bool
	AllowFrom []string
}

// FeishuGroupRule defines per-group access settings.
type FeishuGroupRule struct {
	Enabled        *bool
	AllowFrom      []string
	RequireMention *bool
}

// FeishuConfig controls Feishu channel integration.
type FeishuConfig struct {
	Name           string
	AppID          string
	AppSecret      string
	BaseURL        string
	Enabled        bool
	TextChunkLimit int
	DMPolicy       string
	AllowFrom      []string
	GroupPolicy    string
	GroupAllowFrom []string
	Groups         map[string]FeishuGroupRule
	RequireMention *bool

	// Per-instance SOUL content (from SOUL-<name>.md, falls back to global).
	SoulContent  string
	SoulPath     string
	SoulOverride bool
}

// WeComConfig controls WeCom channel integration.
type WeComConfig struct {
	Name           string
	Token          string
	EncodingAESKey string
	Enabled        bool
	WebhookPath    string                    // required when webhook mode is enabled
	TextChunkLimit int                       // max UTF-8 bytes per chunk (default 4096)
	DMPolicy       string                    // "pairing" (default), "open", "allowlist"
	AllowFrom      []string                  // UserID strings
	GroupPolicy    string                    // "allowlist" (default), "open", "disabled"
	GroupAllowFrom []string                  // group-level chatID allowlist
	Groups         map[string]WeComGroupRule // chatID → rule; "*" = wildcard fallback

	// WebSocket mode fields.
	ConnectionMode    string        // "webhook" (default) or "websocket"
	BotID             string        // required for websocket
	Secret            string        // required for websocket
	WSURL             string        // optional, default wss://openws.work.weixin.qq.com
	HeartbeatInterval time.Duration // optional, default 30s

	// Per-instance SOUL content (from SOUL-<name>.md, falls back to global).
	SoulContent  string
	SoulPath     string
	SoulOverride bool
	SoulAppend   string
}

// WeixinConfig controls Weixin (personal WeChat) channel integration.
type WeixinConfig struct {
	Name           string
	BaseURL        string // API base URL (default: https://oai.ilink.bot)
	Token          string // iLink bot token
	Enabled        bool
	TextChunkLimit int      // max runes per chunk (default 4000)
	DMPolicy       string   // "pairing" (default), "open", "allowlist"
	AllowFrom      []string // user ID strings (xxx@im.wechat)

	// Per-instance SOUL content (from SOUL-<name>.md, falls back to global).
	SoulContent  string
	SoulPath     string
	SoulOverride bool
}

// QQBotConfig controls QQ Bot channel integration.
type QQBotConfig struct {
	Name           string
	AppID          string
	ClientSecret   string
	Enabled        bool
	DMPolicy       string   // "open" (default), "pairing", "allowlist"
	AllowFrom      []string // user openid allowlist
	GroupPolicy    string   // "allowlist" (default), "open", "disabled"
	GroupAllowFrom []string // group openid allowlist
	TextChunkLimit int      // max chars per chunk (default 5000)

	// Per-instance SOUL content (from SOUL-<name>.md, falls back to global).
	SoulContent  string
	SoulPath     string
	SoulOverride bool
}

// Load builds configuration by merging the config file (~/.clawdex/clawdex.json)
// with environment variables. Env vars always take precedence over the file.
func Load() (*Config, error) {
	fileCfg, err := onboard.LoadFileConfig()
	if err != nil {
		return nil, fmt.Errorf("load config file: %w", err)
	}

	workDir := strings.TrimSpace(os.Getenv("CODEX_WORKDIR"))
	if workDir == "" {
		workDir = fileCfg.Codex.WorkDir
	}
	if workDir == "" {
		workDir, err = daemon.WorkspaceDir()
		if err != nil {
			return nil, fmt.Errorf("resolve default CODEX_WORKDIR: %w", err)
		}
	}
	if !filepath.IsAbs(workDir) {
		abs, err := filepath.Abs(workDir)
		if err != nil {
			return nil, fmt.Errorf("resolve absolute CODEX_WORKDIR: %w", err)
		}
		workDir = abs
	}
	if stat, err := os.Stat(workDir); err != nil || !stat.IsDir() {
		if err != nil {
			return nil, fmt.Errorf("CODEX_WORKDIR is invalid: %w", err)
		}
		return nil, errors.New("CODEX_WORKDIR must be a directory")
	}

	address := envOr("GATEWAY_ADDR", fileCfg.Gateway.Address)
	if address == "" {
		address = ":8080"
	}

	commandTimeout := 120 * time.Minute
	timeoutStr := envOr("CODEX_TIMEOUT", fileCfg.Codex.Timeout)
	if timeoutStr != "" {
		d, err := time.ParseDuration(timeoutStr)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("invalid CODEX_TIMEOUT: %s", timeoutStr)
		}
		commandTimeout = d
	}

	sandbox := envOr("CODEX_SANDBOX", fileCfg.Codex.Sandbox)
	if sandbox == "" {
		sandbox = "workspace-write"
	}
	switch sandbox {
	case "read-only", "workspace-write", "danger-full-access":
		// valid
	default:
		return nil, fmt.Errorf("invalid codex sandbox value %q: must be read-only, workspace-write, or danger-full-access", sandbox)
	}

	groupSandbox := envOr("CODEX_GROUP_SANDBOX", fileCfg.Codex.GroupSandbox)
	if groupSandbox != "" {
		switch groupSandbox {
		case "read-only", "workspace-write", "danger-full-access":
			// valid
		default:
			return nil, fmt.Errorf("invalid codex group_sandbox value %q: must be read-only, workspace-write, or danger-full-access", groupSandbox)
		}
	}

	// Load SOUL.md from ~/.clawdex/SOUL.md (best-effort, not an error if missing).
	var soulContent string
	var soulPath string
	if path, err := onboard.SoulPath(); err == nil {
		soulPath = path
		if data, err := os.ReadFile(soulPath); err == nil {
			soulContent = strings.TrimSpace(string(data))
		}
	}

	cfg := &Config{
		Server: ServerConfig{Address: address},
		Logging: LoggingConfig{
			Level:     envOr("LOG_LEVEL", fileCfg.Logging.Level),
			Format:    envOr("LOG_FORMAT", fileCfg.Logging.Format),
			CodexFile: envOr("LOG_CODEX_FILE", fileCfg.Logging.CodexFile),
		},
		Codex: CodexConfig{
			WorkDir:        workDir,
			CommandTimeout: commandTimeout,
			Sandbox:        sandbox,
			GroupSandbox:   groupSandbox,
			SoulContent:    soulContent,
			SoulPath:       soulPath,
		},
	}

	// Apply logging defaults.
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if cfg.Logging.Format == "" {
		cfg.Logging.Format = "text"
	}
	// Validate logging config.
	switch cfg.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return nil, fmt.Errorf("invalid log level %q: must be debug, info, warn, or error", cfg.Logging.Level)
	}
	switch cfg.Logging.Format {
	case "text", "json":
	default:
		return nil, fmt.Errorf("invalid log format %q: must be text or json", cfg.Logging.Format)
	}

	// ── Telegram (multi-instance) ──

	// Load from channels format.
	for name, raw := range fileCfg.Channels {
		chType, _ := onboard.ChannelType(raw)
		if chType == "telegram" {
			var ch onboard.TelegramChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				return nil, fmt.Errorf("channel %q: parse telegram config: %w", name, err)
			}
			tgCfg, err := loadTelegramFromChannel(name, ch, soulContent)
			if err != nil {
				return nil, fmt.Errorf("channel %q: %w", name, err)
			}
			cfg.Telegram = append(cfg.Telegram, *tgCfg)
		}
	}

	// Validate instance names are unique.
	tgNames := make(map[string]bool, len(cfg.Telegram))
	for _, tg := range cfg.Telegram {
		if tgNames[tg.Name] {
			return nil, fmt.Errorf("duplicate telegram instance name %q", tg.Name)
		}
		tgNames[tg.Name] = true
	}

	// ── WeCom (multi-instance) ──

	// Load from channels format.
	for name, raw := range fileCfg.Channels {
		chType, _ := onboard.ChannelType(raw)
		if chType == "wecom" {
			var ch onboard.WeComChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				return nil, fmt.Errorf("channel %q: parse wecom config: %w", name, err)
			}
			wcCfg, err := loadWeComFromChannel(name, ch, soulContent)
			if err != nil {
				return nil, fmt.Errorf("channel %q: %w", name, err)
			}
			cfg.WeCom = append(cfg.WeCom, *wcCfg)
		}
	}

	// Validate instance names are unique and webhook paths are unique.
	wcNames := make(map[string]bool, len(cfg.WeCom))
	wcPaths := make(map[string]bool, len(cfg.WeCom))
	for _, wc := range cfg.WeCom {
		if wcNames[wc.Name] {
			return nil, fmt.Errorf("duplicate wecom instance name %q", wc.Name)
		}
		wcNames[wc.Name] = true
		if wc.Enabled && wc.ConnectionMode != "websocket" && wc.WebhookPath != "" {
			if wcPaths[wc.WebhookPath] {
				return nil, fmt.Errorf("duplicate wecom webhook path %q", wc.WebhookPath)
			}
			wcPaths[wc.WebhookPath] = true
		}
	}

	// ── Weixin (multi-instance) ──

	for name, raw := range fileCfg.Channels {
		chType, _ := onboard.ChannelType(raw)
		if chType == "weixin" {
			var ch onboard.WeixinChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				return nil, fmt.Errorf("channel %q: parse weixin config: %w", name, err)
			}
			wxCfg, err := loadWeixinFromChannel(name, ch, soulContent)
			if err != nil {
				return nil, fmt.Errorf("channel %q: %w", name, err)
			}
			cfg.Weixin = append(cfg.Weixin, *wxCfg)
		}
	}

	// Validate Weixin instance names are unique.
	wxNames := make(map[string]bool, len(cfg.Weixin))
	for _, wx := range cfg.Weixin {
		if wxNames[wx.Name] {
			return nil, fmt.Errorf("duplicate weixin instance name %q", wx.Name)
		}
		wxNames[wx.Name] = true
	}

	// ── QQ Bot (multi-instance) ──

	for name, raw := range fileCfg.Channels {
		chType, _ := onboard.ChannelType(raw)
		if chType == "qqbot" {
			var ch onboard.QQBotChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				return nil, fmt.Errorf("channel %q: parse qqbot config: %w", name, err)
			}
			qqCfg := loadQQBotFromChannel(name, ch, soulContent)
			cfg.QQBot = append(cfg.QQBot, *qqCfg)
		}
	}

	// Validate QQ Bot instance names are unique.
	qqNames := make(map[string]bool, len(cfg.QQBot))
	for _, qq := range cfg.QQBot {
		if qqNames[qq.Name] {
			return nil, fmt.Errorf("duplicate qqbot instance name %q", qq.Name)
		}
		qqNames[qq.Name] = true
	}

	// ── Feishu (multi-instance) ──

	for name, raw := range fileCfg.Channels {
		chType, _ := onboard.ChannelType(raw)
		if chType == "feishu" {
			var ch onboard.FeishuChannelConfig
			if err := json.Unmarshal(raw, &ch); err != nil {
				return nil, fmt.Errorf("channel %q: parse feishu config: %w", name, err)
			}
			fsCfg, err := loadFeishuFromChannel(name, ch, soulContent)
			if err != nil {
				return nil, fmt.Errorf("channel %q: %w", name, err)
			}
			cfg.Feishu = append(cfg.Feishu, *fsCfg)
		}
	}

	// Validate Feishu instance names are unique.
	fsNames := make(map[string]bool, len(cfg.Feishu))
	for _, fs := range cfg.Feishu {
		if fsNames[fs.Name] {
			return nil, fmt.Errorf("duplicate feishu instance name %q", fs.Name)
		}
		fsNames[fs.Name] = true
	}

	// Validate: at least one channel must be enabled.
	tgAnyEnabled := false
	for _, tg := range cfg.Telegram {
		if tg.Enabled {
			tgAnyEnabled = true
			break
		}
	}
	wecomAnyEnabled := false
	for _, wc := range cfg.WeCom {
		if wc.Enabled {
			wecomAnyEnabled = true
			break
		}
	}
	weixinAnyEnabled := false
	for _, wx := range cfg.Weixin {
		if wx.Enabled {
			weixinAnyEnabled = true
			break
		}
	}
	qqbotAnyEnabled := false
	for _, qq := range cfg.QQBot {
		if qq.Enabled {
			qqbotAnyEnabled = true
			break
		}
	}
	feishuAnyEnabled := false
	for _, fs := range cfg.Feishu {
		if fs.Enabled {
			feishuAnyEnabled = true
			break
		}
	}
	if !tgAnyEnabled && !wecomAnyEnabled && !weixinAnyEnabled && !qqbotAnyEnabled && !feishuAnyEnabled {
		return nil, errors.New("at least one channel must be enabled (telegram, wecom, weixin, qqbot, or feishu)")
	}

	// Telegram token is only required when Telegram is enabled.
	for _, tg := range cfg.Telegram {
		if tg.Enabled && tg.BotToken == "" {
			return nil, fmt.Errorf("telegram channel %q: bot token is required when enabled (run 'clawdex onboard' to configure)", tg.Name)
		}
	}

	return cfg, nil
}

// loadTelegramFromChannel builds a TelegramConfig from the new channels format.
func loadTelegramFromChannel(name string, ch onboard.TelegramChannelConfig, globalSoul string) (*TelegramConfig, error) {
	token, err := secret.Resolve(ch.BotToken)
	if err != nil {
		return nil, fmt.Errorf("resolve bot token: %w", err)
	}

	cfg := &TelegramConfig{
		Name:                name,
		BotToken:            token,
		Enabled:             true,
		DMPolicy:            "pairing",
		PollTimeout:         30,
		StartupProbeTimeout: 8 * time.Second,
	}

	if ch.Enabled != nil {
		cfg.Enabled = *ch.Enabled
	}
	if v := strings.TrimSpace(os.Getenv("TELEGRAM_ENABLED")); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			cfg.Enabled = true
		case "false", "0":
			cfg.Enabled = false
		default:
			return nil, fmt.Errorf("invalid TELEGRAM_ENABLED %q: must be true or false", v)
		}
	}

	if ch.DMPolicy != "" {
		cfg.DMPolicy = ch.DMPolicy
	}
	cfg.DMPolicy = envOr("TELEGRAM_DM_POLICY", cfg.DMPolicy)
	switch cfg.DMPolicy {
	case "open", "pairing", "allowlist":
	default:
		return nil, fmt.Errorf("invalid dm_policy %q: must be open, pairing, or allowlist", cfg.DMPolicy)
	}
	if cfg.DMPolicy == "allowlist" && len(ch.AllowFrom) == 0 {
		if strings.TrimSpace(os.Getenv("TELEGRAM_ALLOW_FROM")) == "" {
			return nil, errors.New("dm_policy is \"allowlist\" but allow_from is empty")
		}
	}

	if v := strings.TrimSpace(os.Getenv("TELEGRAM_POLL_TIMEOUT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 50 {
			return nil, errors.New("TELEGRAM_POLL_TIMEOUT must be an integer between 1 and 50")
		}
		cfg.PollTimeout = n
	}

	if v := strings.TrimSpace(os.Getenv("TELEGRAM_STARTUP_PROBE_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, errors.New("TELEGRAM_STARTUP_PROBE_TIMEOUT must be a valid duration, for example 8s")
		}
		cfg.StartupProbeTimeout = d
	}

	cfg.ChunkMode = envOr("TELEGRAM_CHUNK_MODE", ch.ChunkMode)
	if cfg.ChunkMode == "" {
		cfg.ChunkMode = "length"
	}
	switch cfg.ChunkMode {
	case "length", "newline":
	default:
		return nil, fmt.Errorf("invalid chunk_mode %q: must be length or newline", cfg.ChunkMode)
	}

	if ch.TextChunkLimit > 0 {
		cfg.TextChunkLimit = ch.TextChunkLimit
	}
	if v := strings.TrimSpace(os.Getenv("TELEGRAM_TEXT_CHUNK_LIMIT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 100 {
			return nil, errors.New("TELEGRAM_TEXT_CHUNK_LIMIT must be an integer >= 100")
		}
		cfg.TextChunkLimit = n
	}

	cfg.Streaming = envOr("TELEGRAM_STREAMING", ch.Streaming)
	if cfg.Streaming == "" {
		cfg.Streaming = "partial"
	}
	switch cfg.Streaming {
	case "off", "partial", "progress":
	default:
		return nil, fmt.Errorf("invalid streaming %q: must be off, partial, or progress", cfg.Streaming)
	}

	cfg.AllowFrom = ch.AllowFrom
	if v := strings.TrimSpace(os.Getenv("TELEGRAM_ALLOW_FROM")); v != "" {
		ids, err := parseCommaSepInt64(v)
		if err != nil {
			return nil, fmt.Errorf("invalid TELEGRAM_ALLOW_FROM: %w", err)
		}
		cfg.AllowFrom = ids
	}

	// ── Group configuration ──
	groupPolicy := ch.GroupPolicy
	if groupPolicy == "" {
		groupPolicy = "allowlist"
	}
	groupPolicy = envOr("TELEGRAM_GROUP_POLICY", groupPolicy)
	switch groupPolicy {
	case "open", "allowlist", "disabled":
	default:
		return nil, fmt.Errorf("invalid group_policy %q: must be open, allowlist, or disabled", groupPolicy)
	}
	cfg.GroupPolicy = groupPolicy

	cfg.GroupAllowFrom = ch.GroupAllowFrom
	if v := strings.TrimSpace(os.Getenv("TELEGRAM_GROUP_ALLOW_FROM")); v != "" {
		ids, err := parseCommaSepInt64(v)
		if err != nil {
			return nil, fmt.Errorf("invalid TELEGRAM_GROUP_ALLOW_FROM: %w", err)
		}
		cfg.GroupAllowFrom = ids
	}

	if ch.Groups != nil {
		cfg.Groups = make(map[int64]TelegramGroupRule, len(ch.Groups))
		for k, v := range ch.Groups {
			// Parse string key to int64.
			chatID, err := strconv.ParseInt(k, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid group chat_id %q: %w", k, err)
			}
			cfg.Groups[chatID] = TelegramGroupRule{
				Enabled:        v.Enabled,
				AllowFrom:      v.AllowFrom,
				RequireMention: v.RequireMention,
			}
		}
	}

	cfg.RequireMention = ch.RequireMention

	cfg.SoulContent = globalSoul
	if instancePath, err := onboard.InstanceSoulPath(name); err == nil && instancePath != "" {
		cfg.SoulPath = instancePath
		if data, err := os.ReadFile(instancePath); err == nil {
			cfg.SoulContent = strings.TrimSpace(string(data))
			cfg.SoulOverride = true
		}
	}

	return cfg, nil
}

// loadWeComFromChannel builds a WeComConfig from the channels format.
func loadWeComFromChannel(name string, ch onboard.WeComChannelConfig, globalSoul string) (*WeComConfig, error) {
	wc := &WeComConfig{Name: name}

	// Enabled: default false.
	if ch.Enabled != nil {
		wc.Enabled = *ch.Enabled
	}

	if wc.Enabled {
		connMode := ch.ConnectionMode
		if connMode == "" {
			connMode = "webhook"
		}
		switch connMode {
		case "webhook", "websocket":
		default:
			return nil, fmt.Errorf("invalid connection_mode %q: must be webhook or websocket", connMode)
		}
		wc.ConnectionMode = connMode

		if wc.ConnectionMode == "websocket" {
			resolvedBotID, err := secret.Resolve(ch.BotID)
			if err != nil {
				return nil, fmt.Errorf("resolve botid: %w", err)
			}
			if resolvedBotID == "" {
				return nil, errors.New("websocket mode requires botid")
			}
			wc.BotID = resolvedBotID

			resolvedSecret, err := secret.Resolve(ch.Secret)
			if err != nil {
				return nil, fmt.Errorf("resolve secret: %w", err)
			}
			if resolvedSecret == "" {
				return nil, errors.New("websocket mode requires secret")
			}
			wc.Secret = resolvedSecret

			wc.WSURL = ch.WSURL
			if ch.HeartbeatInt != "" {
				d, err := time.ParseDuration(ch.HeartbeatInt)
				if err != nil || d <= 0 {
					return nil, fmt.Errorf("invalid heartbeat_interval %q", ch.HeartbeatInt)
				}
				wc.HeartbeatInterval = d
			}

			if ch.Token != "" {
				resolved, err := secret.Resolve(ch.Token)
				if err != nil {
					return nil, fmt.Errorf("resolve token: %w", err)
				}
				wc.Token = resolved
			}
			if ch.EncodingAESKey != "" {
				resolvedKey, err := secret.Resolve(ch.EncodingAESKey)
				if err != nil {
					return nil, fmt.Errorf("resolve encoding_aes_key: %w", err)
				}
				wc.EncodingAESKey = resolvedKey
			}
		} else {
			resolved, err := secret.Resolve(ch.Token)
			if err != nil {
				return nil, fmt.Errorf("resolve token: %w", err)
			}
			if resolved == "" {
				return nil, errors.New("enabled but token is empty")
			}
			wc.Token = resolved

			resolvedKey, err := secret.Resolve(ch.EncodingAESKey)
			if err != nil {
				return nil, fmt.Errorf("resolve encoding_aes_key: %w", err)
			}
			if resolvedKey == "" {
				return nil, errors.New("enabled but encoding_aes_key is empty")
			}
			wc.EncodingAESKey = resolvedKey
		}
	}

	wc.WebhookPath = ch.WebhookPath
	if wc.Enabled && wc.ConnectionMode != "websocket" {
		resolved, err := secret.Resolve(wc.WebhookPath)
		if err != nil {
			return nil, fmt.Errorf("resolve webhook_path: %w", err)
		}
		if resolved == "" {
			return nil, errors.New("enabled but webhook_path is empty")
		}
		wc.WebhookPath = resolved
	}

	if ch.TextChunkLimit > 0 {
		wc.TextChunkLimit = ch.TextChunkLimit
	}

	dmPolicy := ch.DMPolicy
	if dmPolicy == "" {
		dmPolicy = "pairing"
	}
	switch dmPolicy {
	case "open", "pairing", "allowlist":
	default:
		return nil, fmt.Errorf("invalid dm_policy %q: must be open, pairing, or allowlist", dmPolicy)
	}
	wc.DMPolicy = dmPolicy

	wc.AllowFrom = ch.AllowFrom

	groupPolicy := ch.GroupPolicy
	if groupPolicy == "" {
		groupPolicy = "allowlist"
	}
	switch groupPolicy {
	case "open", "allowlist", "disabled":
	default:
		return nil, fmt.Errorf("invalid group_policy %q: must be open, allowlist, or disabled", groupPolicy)
	}
	wc.GroupPolicy = groupPolicy

	wc.GroupAllowFrom = ch.GroupAllowFrom

	if ch.Groups != nil {
		wc.Groups = make(map[string]WeComGroupRule, len(ch.Groups))
		for k, v := range ch.Groups {
			wc.Groups[k] = WeComGroupRule{Enabled: v.Enabled, AllowFrom: v.AllowFrom}
		}
	}

	wc.SoulContent = globalSoul
	if instancePath, err := onboard.InstanceSoulPath(name); err == nil && instancePath != "" {
		wc.SoulPath = instancePath
		if data, err := os.ReadFile(instancePath); err == nil {
			wc.SoulContent = strings.TrimSpace(string(data))
			wc.SoulOverride = true
		}
	}

	// In WebSocket mode, append media-handling hints so
	// Codex knows about WeCom voice/video format constraints.
	if wc.ConnectionMode == "websocket" {
		wc.SoulAppend = strings.TrimSpace(wecomMediaHint)
		wc.SoulContent = appendWeComMediaHint(wc.SoulContent)
	}

	return wc, nil
}

// wecomMediaHint tells Codex about WeCom media format constraints
// so it can convert files (e.g. via ffmpeg) before returning them.
const wecomMediaHint = `
## WeCom media constraints

IMPORTANT: To send a file to the user, simply include its
absolute path in your text reply (e.g. /tmp/output.amr). The
system automatically detects file paths in your response and
uploads them via WeCom. Do NOT try to call any WeCom API or
WebSocket yourself.

Format requirements for media files:
- Voice: must be AMR (.amr), max 2 MB. Convert other audio
  formats first:
  ffmpeg -y -i input.wav -ac 1 -ar 8000 -ab 12.2k -c:a libopencore_amrnb output.amr
- Video: must be MP4 (.mp4), max 10 MB.
- Image: jpg/png/gif, max 2 MB.
- Other files: max 20 MB.
`

func appendWeComMediaHint(soul string) string {
	if strings.Contains(soul, "WeCom media constraints") {
		return soul
	}
	if soul == "" {
		return strings.TrimSpace(wecomMediaHint)
	}
	return soul + "\n" + strings.TrimSpace(wecomMediaHint)
}

// loadWeixinFromChannel builds a WeixinConfig from the channels format.
func loadWeixinFromChannel(name string, ch onboard.WeixinChannelConfig, globalSoul string) (*WeixinConfig, error) {
	wx := &WeixinConfig{Name: name}

	// Enabled: default true.
	if ch.Enabled != nil {
		wx.Enabled = *ch.Enabled
	} else {
		wx.Enabled = true
	}

	// Token is obtained via QR login — may be empty until `clawdex weixin login`.
	if ch.Token != "" {
		resolved, err := secret.Resolve(ch.Token)
		if err != nil {
			return nil, fmt.Errorf("resolve token: %w", err)
		}
		wx.Token = resolved
	}

	wx.BaseURL = ch.BaseURL

	if ch.TextChunkLimit > 0 {
		wx.TextChunkLimit = ch.TextChunkLimit
	}

	dmPolicy := ch.DMPolicy
	if dmPolicy == "" {
		dmPolicy = "pairing"
	}
	switch dmPolicy {
	case "open", "pairing", "allowlist":
	default:
		return nil, fmt.Errorf("invalid dm_policy %q: must be open, pairing, or allowlist", dmPolicy)
	}
	wx.DMPolicy = dmPolicy

	wx.AllowFrom = ch.AllowFrom

	wx.SoulContent = globalSoul
	if instancePath, err := onboard.InstanceSoulPath(name); err == nil && instancePath != "" {
		wx.SoulPath = instancePath
		if data, err := os.ReadFile(instancePath); err == nil {
			wx.SoulContent = strings.TrimSpace(string(data))
			wx.SoulOverride = true
		}
	}

	return wx, nil
}

func loadQQBotFromChannel(name string, ch onboard.QQBotChannelConfig, globalSoul string) *QQBotConfig {
	qq := &QQBotConfig{Name: name}

	if ch.Enabled != nil {
		qq.Enabled = *ch.Enabled
	} else {
		qq.Enabled = true
	}

	if ch.AppID != "" {
		resolved, err := secret.Resolve(ch.AppID)
		if err == nil {
			qq.AppID = resolved
		} else {
			qq.AppID = ch.AppID
		}
	}
	if ch.ClientSecret != "" {
		resolved, err := secret.Resolve(ch.ClientSecret)
		if err == nil {
			qq.ClientSecret = resolved
		} else {
			qq.ClientSecret = ch.ClientSecret
		}
	}

	if ch.TextChunkLimit > 0 {
		qq.TextChunkLimit = ch.TextChunkLimit
	}

	dmPolicy := ch.DMPolicy
	if dmPolicy == "" {
		dmPolicy = "open"
	}
	qq.DMPolicy = dmPolicy

	groupPolicy := ch.GroupPolicy
	if groupPolicy == "" {
		groupPolicy = "allowlist"
	}
	qq.GroupPolicy = groupPolicy

	qq.AllowFrom = ch.AllowFrom
	qq.GroupAllowFrom = ch.GroupAllowFrom

	qq.SoulContent = globalSoul
	if instancePath, err := onboard.InstanceSoulPath(name); err == nil && instancePath != "" {
		qq.SoulPath = instancePath
		if data, err := os.ReadFile(instancePath); err == nil {
			qq.SoulContent = strings.TrimSpace(string(data))
			qq.SoulOverride = true
		}
	}

	return qq
}

func loadFeishuFromChannel(name string, ch onboard.FeishuChannelConfig, globalSoul string) (*FeishuConfig, error) {
	fs := &FeishuConfig{Name: name}

	if ch.Enabled != nil {
		fs.Enabled = *ch.Enabled
	} else {
		fs.Enabled = true
	}
	if v := strings.TrimSpace(os.Getenv("FEISHU_ENABLED")); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			fs.Enabled = true
		case "false", "0":
			fs.Enabled = false
		default:
			return nil, fmt.Errorf("invalid FEISHU_ENABLED %q: must be true or false", v)
		}
	}

	if ch.AppID != "" {
		resolved, err := secret.Resolve(ch.AppID)
		if err != nil {
			return nil, fmt.Errorf("resolve app_id: %w", err)
		}
		fs.AppID = resolved
	}
	if v := strings.TrimSpace(os.Getenv("FEISHU_APP_ID")); v != "" {
		fs.AppID = v
	}

	if ch.AppSecret != "" {
		resolved, err := secret.Resolve(ch.AppSecret)
		if err != nil {
			return nil, fmt.Errorf("resolve app_secret: %w", err)
		}
		fs.AppSecret = resolved
	}
	if v := strings.TrimSpace(os.Getenv("FEISHU_APP_SECRET")); v != "" {
		fs.AppSecret = v
	}

	if fs.Enabled {
		if fs.AppID == "" {
			return nil, errors.New("enabled but app_id is empty")
		}
		if fs.AppSecret == "" {
			return nil, errors.New("enabled but app_secret is empty")
		}
	}

	fs.BaseURL = envOr("FEISHU_BASE_URL", ch.BaseURL)

	if ch.TextChunkLimit > 0 {
		fs.TextChunkLimit = ch.TextChunkLimit
	}
	if v := strings.TrimSpace(os.Getenv("FEISHU_TEXT_CHUNK_LIMIT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 100 {
			return nil, errors.New("FEISHU_TEXT_CHUNK_LIMIT must be an integer >= 100")
		}
		fs.TextChunkLimit = n
	}

	dmPolicy := ch.DMPolicy
	if dmPolicy == "" {
		dmPolicy = "pairing"
	}
	dmPolicy = envOr("FEISHU_DM_POLICY", dmPolicy)
	switch dmPolicy {
	case "open", "pairing", "allowlist":
	default:
		return nil, fmt.Errorf("invalid dm_policy %q: must be open, pairing, or allowlist", dmPolicy)
	}
	fs.DMPolicy = dmPolicy

	fs.AllowFrom = ch.AllowFrom
	if v := strings.TrimSpace(os.Getenv("FEISHU_ALLOW_FROM")); v != "" {
		fs.AllowFrom = parseCommaSepStrings(v)
	}

	groupPolicy := ch.GroupPolicy
	if groupPolicy == "" {
		groupPolicy = "allowlist"
	}
	groupPolicy = envOr("FEISHU_GROUP_POLICY", groupPolicy)
	switch groupPolicy {
	case "open", "allowlist", "disabled":
	default:
		return nil, fmt.Errorf("invalid group_policy %q: must be open, allowlist, or disabled", groupPolicy)
	}
	fs.GroupPolicy = groupPolicy

	fs.GroupAllowFrom = ch.GroupAllowFrom
	if v := strings.TrimSpace(os.Getenv("FEISHU_GROUP_ALLOW_FROM")); v != "" {
		fs.GroupAllowFrom = parseCommaSepStrings(v)
	}

	fs.RequireMention = ch.RequireMention
	if v := strings.TrimSpace(os.Getenv("FEISHU_REQUIRE_MENTION")); v != "" {
		switch strings.ToLower(v) {
		case "true", "1":
			b := true
			fs.RequireMention = &b
		case "false", "0":
			b := false
			fs.RequireMention = &b
		default:
			return nil, fmt.Errorf("invalid FEISHU_REQUIRE_MENTION %q: must be true or false", v)
		}
	}
	if fs.RequireMention == nil {
		b := true
		fs.RequireMention = &b
	}

	if ch.Groups != nil {
		fs.Groups = make(map[string]FeishuGroupRule, len(ch.Groups))
		for k, v := range ch.Groups {
			fs.Groups[k] = FeishuGroupRule{
				Enabled:        v.Enabled,
				AllowFrom:      v.AllowFrom,
				RequireMention: v.RequireMention,
			}
		}
	}

	fs.SoulContent = globalSoul
	if instancePath, err := onboard.InstanceSoulPath(name); err == nil && instancePath != "" {
		fs.SoulPath = instancePath
		if data, err := os.ReadFile(instancePath); err == nil {
			fs.SoulContent = strings.TrimSpace(string(data))
			fs.SoulOverride = true
		}
	}

	return fs, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseCommaSepInt64(s string) ([]int64, error) {
	parts := strings.Split(s, ",")
	var ids []int64
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid int64 %q: %w", p, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseCommaSepStrings(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
