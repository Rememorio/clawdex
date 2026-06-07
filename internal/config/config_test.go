package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/onboard"
)

// writeTestConfig writes a FileConfig to a temp dir and sets up the environment
// so that LoadFileConfig() (via ConfigPath → DataDir → HOME) finds it.
// Returns a cleanup function.
func writeTestConfig(t *testing.T, cfg *onboard.FileConfig) {
	t.Helper()
	dir := t.TempDir()
	clawdexDir := filepath.Join(dir, ".clawdex")
	require.NoError(t, os.MkdirAll(clawdexDir, 0o755))

	path := filepath.Join(clawdexDir, "clawdex.json")
	require.NoError(t, onboard.SaveFileConfigTo(cfg, path))

	t.Setenv("HOME", dir)
}

// firstTelegram returns the first telegram config or fails the test.
func firstTelegram(t *testing.T, cfg *Config) TelegramConfig {
	t.Helper()
	require.NotEmpty(t, cfg.Telegram, "expected at least one telegram channel")
	return cfg.Telegram[0]
}

// tgChannel creates a telegram channel config as json.RawMessage.
func tgChannel(opts ...func(*onboard.TelegramChannelConfig)) json.RawMessage {
	ch := &onboard.TelegramChannelConfig{Type: "telegram"}
	for _, opt := range opts {
		opt(ch)
	}
	return onboard.MarshalTelegramChannel(*ch)
}

// wcChannel creates a wecom channel config as json.RawMessage.
func wcChannel(opts ...func(*onboard.WeComChannelConfig)) json.RawMessage {
	ch := &onboard.WeComChannelConfig{Type: "wecom"}
	for _, opt := range opts {
		opt(ch)
	}
	return onboard.MarshalWeComChannel(*ch)
}

// fsChannel creates a feishu channel config as json.RawMessage.
func fsChannel(opts ...func(*onboard.FeishuChannelConfig)) json.RawMessage {
	ch := &onboard.FeishuChannelConfig{Type: "feishu"}
	for _, opt := range opts {
		opt(ch)
	}
	return onboard.MarshalFeishuChannel(*ch)
}

func TestLoad_PlainToken(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123456:plain-token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "123456:plain-token", firstTelegram(t, cfg).BotToken)
}

func TestLoad_FeishuChannel(t *testing.T) {
	enabled := true
	groupEnabled := true
	groupRequireMention := false
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"fs-main": fsChannel(func(ch *onboard.FeishuChannelConfig) {
				ch.Enabled = &enabled
				ch.AppID = "cli_test"
				ch.AppSecret = "secret"
				ch.DMPolicy = "allowlist"
				ch.AllowFrom = []string{"ou_user"}
				ch.GroupPolicy = "allowlist"
				ch.GroupAllowFrom = []string{"oc_group"}
				ch.TextChunkLimit = 3500
				ch.Groups = map[string]onboard.FeishuGroupRule{
					"oc_group": {
						Enabled:        &groupEnabled,
						AllowFrom:      []string{"ou_user"},
						RequireMention: &groupRequireMention,
					},
				}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.Feishu, 1)
	fs := cfg.Feishu[0]
	assert.Equal(t, "fs-main", fs.Name)
	assert.Equal(t, "cli_test", fs.AppID)
	assert.Equal(t, "secret", fs.AppSecret)
	assert.True(t, fs.Enabled)
	assert.Equal(t, "allowlist", fs.DMPolicy)
	assert.Equal(t, []string{"ou_user"}, fs.AllowFrom)
	assert.Equal(t, "allowlist", fs.GroupPolicy)
	assert.Equal(t, []string{"oc_group"}, fs.GroupAllowFrom)
	assert.Equal(t, 3500, fs.TextChunkLimit)
	require.NotNil(t, fs.RequireMention)
	assert.True(t, *fs.RequireMention)
	require.Contains(t, fs.Groups, "oc_group")
	assert.Equal(t, []string{"ou_user"}, fs.Groups["oc_group"].AllowFrom)
	require.NotNil(t, fs.Groups["oc_group"].RequireMention)
	assert.False(t, *fs.Groups["oc_group"].RequireMention)
}

func TestLoad_EnvRefToken(t *testing.T) {
	t.Setenv("MY_TG_TOKEN", "resolved-from-env")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "${MY_TG_TOKEN}"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "resolved-from-env", firstTelegram(t, cfg).BotToken)
}

func TestLoad_FileRefToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	require.NoError(t, os.WriteFile(tokenFile, []byte("999:file-token\n"), 0o644))

	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "file://" + tokenFile
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "999:file-token", firstTelegram(t, cfg).BotToken)
}

func TestLoad_MissingToken(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{})

	_, err := Load()
	assert.ErrorContains(t, err, "at least one channel must be enabled")
}

func TestLoad_EnvRefTokenUnset(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "${UNSET_VAR_12345}"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "UNSET_VAR_12345")
}

func TestLoad_CodexDefaults(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	expectedWorkDir := filepath.Join(os.Getenv("HOME"), ".clawdex", "workspace")
	assert.Equal(t, expectedWorkDir, cfg.Codex.WorkDir)
	assert.DirExists(t, expectedWorkDir)
	assert.Equal(t, 120*60*1e9, float64(cfg.Codex.CommandTimeout)) // 120m in ns
}

func TestLoad_CodexFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{
			Timeout: "30m",
		},
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 30*60*1e9, float64(cfg.Codex.CommandTimeout)) // 30m in ns
}

func TestLoad_CodexEnvOverridesFile(t *testing.T) {
	t.Setenv("CODEX_TIMEOUT", "5m")
	writeTestConfig(t, &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{
			Timeout: "30m",
		},
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 5*60*1e9, float64(cfg.Codex.CommandTimeout)) // 5m
}

func TestLoad_AllowFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.AllowFrom = []int64{100, 200}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []int64{100, 200}, firstTelegram(t, cfg).AllowFrom)
}

func TestLoad_AllowFromEnvOverridesFile(t *testing.T) {
	t.Setenv("TELEGRAM_ALLOW_FROM", "111,222")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.AllowFrom = []int64{100}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []int64{111, 222}, firstTelegram(t, cfg).AllowFrom)
}

func TestLoad_GatewayAddressFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Gateway: onboard.GatewayFileConfig{
			Address: ":9090",
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.Server.Address)
}

func TestLoad_GatewayAddressDefault(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, ":8080", cfg.Server.Address)
}

func TestLoad_LoggingDefaults(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "info", cfg.Logging.Level)
	assert.Equal(t, "text", cfg.Logging.Format)
	assert.Equal(t, "", cfg.Logging.CodexFile)
}

func TestLoad_LoggingCodexFileFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Logging: onboard.LoggingFileConfig{CodexFile: "/tmp/codex.log"},
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/codex.log", cfg.Logging.CodexFile)
}

func TestLoad_LoggingCodexFileEnvOverride(t *testing.T) {
	t.Setenv("LOG_CODEX_FILE", "/tmp/from-env.log")
	writeTestConfig(t, &onboard.FileConfig{
		Logging: onboard.LoggingFileConfig{CodexFile: "/tmp/from-file.log"},
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/from-env.log", cfg.Logging.CodexFile)
}

func TestLoad_InvalidCodexTimeout(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{
			Timeout: "not-a-duration",
		},
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid CODEX_TIMEOUT")
}

func TestLoad_PollTimeoutFromEnv(t *testing.T) {
	t.Setenv("TELEGRAM_POLL_TIMEOUT", "10")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 10, firstTelegram(t, cfg).PollTimeout)
}

func TestLoad_PollTimeoutInvalid(t *testing.T) {
	t.Setenv("TELEGRAM_POLL_TIMEOUT", "999")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "TELEGRAM_POLL_TIMEOUT")
}

func TestLoad_StartupProbeTimeoutFromEnv(t *testing.T) {
	t.Setenv("TELEGRAM_STARTUP_PROBE_TIMEOUT", "15s")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, firstTelegram(t, cfg).StartupProbeTimeout)
}

func TestLoad_StartupProbeTimeoutInvalid(t *testing.T) {
	t.Setenv("TELEGRAM_STARTUP_PROBE_TIMEOUT", "bad")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "TELEGRAM_STARTUP_PROBE_TIMEOUT")
}

func TestLoad_InvalidAllowFrom(t *testing.T) {
	t.Setenv("TELEGRAM_ALLOW_FROM", "not-a-number")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "TELEGRAM_ALLOW_FROM")
}

func TestLoad_InvalidCodexTimeoutEnv(t *testing.T) {
	t.Setenv("CODEX_TIMEOUT", "bad")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "CODEX_TIMEOUT")
}

func TestLoad_GatewayAddressEnvOverride(t *testing.T) {
	t.Setenv("GATEWAY_ADDR", ":7777")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Gateway: onboard.GatewayFileConfig{Address: ":9090"},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, ":7777", cfg.Server.Address)
}

func TestLoad_WorkdirFromEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_WORKDIR", dir)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Codex: onboard.CodexFileConfig{WorkDir: "/some/other/path"},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, dir, cfg.Codex.WorkDir)
}

func TestLoad_WorkdirFromFile(t *testing.T) {
	dir := t.TempDir()
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Codex: onboard.CodexFileConfig{WorkDir: dir},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, dir, cfg.Codex.WorkDir)
}

func TestLoad_NoAllowFromDefault(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []int64{}, firstTelegram(t, cfg).AllowFrom)
}

func TestLoad_NegativeCodexTimeout(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{Timeout: "-5m"},
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid CODEX_TIMEOUT")
}

func TestLoad_WorkdirNotExist(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Codex: onboard.CodexFileConfig{WorkDir: "/nonexistent/path/xyz"},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "CODEX_WORKDIR is invalid")
}

func TestLoad_WorkdirIsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))

	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Codex: onboard.CodexFileConfig{WorkDir: f},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "CODEX_WORKDIR must be a directory")
}

func TestLoad_PollTimeoutNonNumeric(t *testing.T) {
	t.Setenv("TELEGRAM_POLL_TIMEOUT", "abc")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "TELEGRAM_POLL_TIMEOUT")
}

func TestLoad_NegativeStartupProbe(t *testing.T) {
	t.Setenv("TELEGRAM_STARTUP_PROBE_TIMEOUT", "-1s")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "TELEGRAM_STARTUP_PROBE_TIMEOUT")
}

func TestLoad_NegativeCodexTimeoutEnv(t *testing.T) {
	t.Setenv("CODEX_TIMEOUT", "-5m")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "CODEX_TIMEOUT")
}

func TestLoad_SandboxDefault(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "workspace-write", cfg.Codex.Sandbox)
}

func TestLoad_SandboxFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Codex: onboard.CodexFileConfig{Sandbox: "read-only"},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "read-only", cfg.Codex.Sandbox)
}

func TestLoad_SandboxEnvOverride(t *testing.T) {
	t.Setenv("CODEX_SANDBOX", "danger-full-access")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Codex: onboard.CodexFileConfig{Sandbox: "read-only"},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "danger-full-access", cfg.Codex.Sandbox)
}

func TestLoad_SandboxInvalid(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Codex: onboard.CodexFileConfig{Sandbox: "invalid-value"},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid codex sandbox value")
}

// ── Enabled and DMPolicy tests ──

func TestLoad_EnabledDefault(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, firstTelegram(t, cfg).Enabled)
}

func TestLoad_EnabledFromFile(t *testing.T) {
	f := false
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.Enabled = &f
			}),
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Token = "wc_token"
				ch.EncodingAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
				ch.WebhookPath = "/wecom/webhook"
				ch.Enabled = &tr
				ch.DMPolicy = "pairing"
				ch.GroupPolicy = "allowlist"
				ch.TextChunkLimit = 4096
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.False(t, firstTelegram(t, cfg).Enabled)
	require.Len(t, cfg.WeCom, 1)
	assert.True(t, cfg.WeCom[0].Enabled)
}

func TestLoad_EnabledEnvOverride(t *testing.T) {
	t.Setenv("TELEGRAM_ENABLED", "false")
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Token = "wc_token"
				ch.EncodingAESKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"
				ch.WebhookPath = "/wecom/webhook"
				ch.Enabled = &tr
				ch.DMPolicy = "pairing"
				ch.GroupPolicy = "allowlist"
				ch.TextChunkLimit = 4096
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.False(t, firstTelegram(t, cfg).Enabled)
	require.Len(t, cfg.WeCom, 1)
	assert.True(t, cfg.WeCom[0].Enabled)
}

func TestLoad_EnabledInvalidEnv(t *testing.T) {
	t.Setenv("TELEGRAM_ENABLED", "maybe")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "TELEGRAM_ENABLED")
}

func TestLoad_DMPolicyDefault(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "pairing", firstTelegram(t, cfg).DMPolicy)
}

func TestLoad_DMPolicyFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.DMPolicy = "open"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "open", firstTelegram(t, cfg).DMPolicy)
}

func TestLoad_DMPolicyEnvOverride(t *testing.T) {
	t.Setenv("TELEGRAM_DM_POLICY", "allowlist")
	t.Setenv("TELEGRAM_ALLOW_FROM", "123")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "allowlist", firstTelegram(t, cfg).DMPolicy)
}

func TestLoad_DMPolicyInvalid(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.DMPolicy = "invalid"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid dm_policy")
}

func TestLoad_AllowlistNoUsers(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.DMPolicy = "allowlist"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "allow_from is empty")
}

func TestLoad_AllowlistWithUsers(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.DMPolicy = "allowlist"
				ch.AllowFrom = []int64{100}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "allowlist", firstTelegram(t, cfg).DMPolicy)
	assert.Equal(t, []int64{100}, firstTelegram(t, cfg).AllowFrom)
}

// ── Multi-instance tests ──

func TestLoad_MultipleTelegramChannels(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg-bot1": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "111:token1"
			}),
			"tg-bot2": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "222:token2"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.Telegram, 2)

	// Find by name.
	var found1, found2 bool
	for _, tg := range cfg.Telegram {
		if tg.Name == "tg-bot1" {
			assert.Equal(t, "111:token1", tg.BotToken)
			found1 = true
		}
		if tg.Name == "tg-bot2" {
			assert.Equal(t, "222:token2", tg.BotToken)
			found2 = true
		}
	}
	assert.True(t, found1, "tg-bot1 not found")
	assert.True(t, found2, "tg-bot2 not found")
}

func TestLoad_DuplicateTelegramNames(t *testing.T) {
	// This is a validation test - duplicate names in the config file map
	// would be caught earlier, but if somehow they existed, Load should error.
	// Note: JSON map keys are unique, so this test validates the name check works.
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Len(t, cfg.Telegram, 1)
	assert.Equal(t, "telegram", cfg.Telegram[0].Name)
}

func TestAppendWeComMediaHint_EmptySoul(t *testing.T) {
	result := appendWeComMediaHint("")
	assert.Contains(t, result, "WeCom media constraints")
	assert.Contains(t, result, "AMR (.amr)")
}

func TestAppendWeComMediaHint_ExistingSoul(t *testing.T) {
	result := appendWeComMediaHint("Be a cat.")
	assert.True(t, len(result) > len("Be a cat."))
	assert.Contains(t, result, "Be a cat.")
	assert.Contains(t, result, "WeCom media constraints")
}

func TestAppendWeComMediaHint_AlreadyPresent(t *testing.T) {
	first := appendWeComMediaHint("Be a cat.")
	second := appendWeComMediaHint(first)
	// Should not duplicate the hint.
	assert.Equal(t, first, second)
}

func TestLoadWeComWebSocket_InjectsMediaHint(t *testing.T) {
	t.Setenv("WECOM_BOTID", "bot123")
	t.Setenv("WECOM_SECRET", "secret456")

	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.ConnectionMode = "websocket"
				ch.BotID = "${WECOM_BOTID}"
				ch.Secret = "${WECOM_SECRET}"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.WeCom, 1)
	assert.Contains(t, cfg.WeCom[0].SoulContent,
		"WeCom media constraints")
}

func TestLoadWeComWebhook_NoMediaHint(t *testing.T) {
	t.Setenv("WECOM_TOKEN", "tok")
	t.Setenv("WECOM_AES",
		"abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG")
	t.Setenv("WECOM_HOOK", "/wecom")

	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.ConnectionMode = "webhook"
				ch.Token = "${WECOM_TOKEN}"
				ch.EncodingAESKey = "${WECOM_AES}"
				ch.WebhookPath = "${WECOM_HOOK}"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.WeCom, 1)
	assert.NotContains(t, cfg.WeCom[0].SoulContent,
		"WeCom media constraints")
}

// ── Weixin channel tests ──

// wxChannel creates a weixin channel config as json.RawMessage.
func wxChannel(opts ...func(*onboard.WeixinChannelConfig)) json.RawMessage {
	ch := &onboard.WeixinChannelConfig{Type: "weixin"}
	for _, opt := range opts {
		opt(ch)
	}
	return onboard.MarshalWeixinChannel(*ch)
}

// qqChannel creates a qqbot channel config as json.RawMessage.
func qqChannel(opts ...func(*onboard.QQBotChannelConfig)) json.RawMessage {
	ch := &onboard.QQBotChannelConfig{Type: "qqbot"}
	for _, opt := range opts {
		opt(ch)
	}
	data, _ := json.Marshal(ch)
	return data
}

func TestLoad_WeixinChannel_Basic(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wx-bot": wxChannel(func(ch *onboard.WeixinChannelConfig) {
				ch.Token = "wx-token-123"
				ch.BaseURL = "https://example.com"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.Weixin, 1)
	assert.Equal(t, "wx-bot", cfg.Weixin[0].Name)
	assert.True(t, cfg.Weixin[0].Enabled)
	assert.Equal(t, "wx-token-123", cfg.Weixin[0].Token)
	assert.Equal(t, "https://example.com", cfg.Weixin[0].BaseURL)
	assert.Equal(t, "open", cfg.Weixin[0].DMPolicy)
}

func TestLoad_WeixinChannel_Disabled(t *testing.T) {
	f := false
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": wxChannel(func(ch *onboard.WeixinChannelConfig) {
				ch.Token = "wx-token"
				ch.Enabled = &f
			}),
			"telegram": tgChannel(func(tg *onboard.TelegramChannelConfig) {
				tg.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.Weixin, 1)
	assert.False(t, cfg.Weixin[0].Enabled)
}

func TestLoad_WeixinChannel_DMPolicyOpen(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": wxChannel(func(ch *onboard.WeixinChannelConfig) {
				ch.Token = "wx-token"
				ch.DMPolicy = "open"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.Weixin, 1)
	assert.Equal(t, "open", cfg.Weixin[0].DMPolicy)
}

func TestLoad_WeixinChannel_DMPolicyInvalid(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": wxChannel(func(ch *onboard.WeixinChannelConfig) {
				ch.Token = "wx-token"
				ch.DMPolicy = "bogus"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid dm_policy")
}

func TestLoad_WeixinChannel_TextChunkLimit(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": wxChannel(func(ch *onboard.WeixinChannelConfig) {
				ch.Token = "wx-token"
				ch.TextChunkLimit = 2000
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.Weixin, 1)
	assert.Equal(t, 2000, cfg.Weixin[0].TextChunkLimit)
}

func TestLoad_WeixinChannel_TokenFromEnv(t *testing.T) {
	t.Setenv("WX_TOKEN", "resolved-wx")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": wxChannel(func(ch *onboard.WeixinChannelConfig) {
				ch.Token = "${WX_TOKEN}"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.Weixin, 1)
	assert.Equal(t, "resolved-wx", cfg.Weixin[0].Token)
}

func TestLoad_WeixinChannel_DuplicateNames(t *testing.T) {
	// JSON map keys must be unique, so we test the multi-instance path.
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wx1": wxChannel(func(ch *onboard.WeixinChannelConfig) {
				ch.Token = "tok1"
			}),
			"wx2": wxChannel(func(ch *onboard.WeixinChannelConfig) {
				ch.Token = "tok2"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Len(t, cfg.Weixin, 2)
}

// ── QQBot channel tests ──

func TestLoad_QQBotChannel_Basic(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"qq": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app123"
				ch.ClientSecret = "secret456"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.QQBot, 1)
	assert.Equal(t, "qq", cfg.QQBot[0].Name)
	assert.True(t, cfg.QQBot[0].Enabled)
	assert.Equal(t, "app123", cfg.QQBot[0].AppID)
	assert.Equal(t, "secret456", cfg.QQBot[0].ClientSecret)
	assert.Equal(t, "open", cfg.QQBot[0].DMPolicy)
	assert.Equal(t, "allowlist", cfg.QQBot[0].GroupPolicy)
}

func TestLoad_QQBotChannel_Disabled(t *testing.T) {
	f := false
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"qq": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app"
				ch.Enabled = &f
			}),
			"telegram": tgChannel(func(tg *onboard.TelegramChannelConfig) {
				tg.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.QQBot, 1)
	assert.False(t, cfg.QQBot[0].Enabled)
}

func TestLoad_QQBotChannel_TextChunkLimit(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"qq": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app"
				ch.ClientSecret = "sec"
				ch.TextChunkLimit = 3000
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.QQBot, 1)
	assert.Equal(t, 3000, cfg.QQBot[0].TextChunkLimit)
}

func TestLoad_QQBotChannel_AppIDFromEnv(t *testing.T) {
	t.Setenv("QQ_APPID", "env-app-id")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"qq": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "${QQ_APPID}"
				ch.ClientSecret = "sec"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.QQBot, 1)
	assert.Equal(t, "env-app-id", cfg.QQBot[0].AppID)
}

func TestLoad_QQBotChannel_MultiInstance(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"qq1": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app1"
				ch.ClientSecret = "sec1"
			}),
			"qq2": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app2"
				ch.ClientSecret = "sec2"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Len(t, cfg.QQBot, 2)
}

func TestLoad_QQBotChannel_GroupAllowFrom(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"qq": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app"
				ch.ClientSecret = "sec"
				ch.GroupPolicy = "allowlist"
				ch.GroupAllowFrom = []string{"group1", "group2"}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.QQBot, 1)
	assert.Equal(t, []string{"group1", "group2"}, cfg.QQBot[0].GroupAllowFrom)
}

// ── parseCommaSepStrings tests ──

func TestParseCommaSepStrings(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{"single value", "hello", []string{"hello"}},
		{"multiple values", "a,b,c", []string{"a", "b", "c"}},
		{"with spaces", " a , b , c ", []string{"a", "b", "c"}},
		{"empty parts skipped", "a,,b", []string{"a", "b"}},
		{"all empty", ",,,", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseCommaSepStrings(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// ── Invalid log level/format tests ──

func TestLoad_InvalidLogLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "trace")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid log level")
}

func TestLoad_InvalidLogFormat(t *testing.T) {
	t.Setenv("LOG_FORMAT", "yaml")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid log format")
}

// ── GroupSandbox tests ──

func TestLoad_GroupSandboxValid(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Codex: onboard.CodexFileConfig{GroupSandbox: "read-only"},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "read-only", cfg.Codex.GroupSandbox)
}

func TestLoad_GroupSandboxInvalid(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
		Codex: onboard.CodexFileConfig{GroupSandbox: "unsafe"},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid codex group_sandbox value")
}

// ── Additional coverage tests (appended) ──

func TestLoad_TelegramGroups(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.GroupPolicy = "allowlist"
				ch.GroupAllowFrom = []int64{-100, -200}
				enabled := true
				requireMention := false
				ch.Groups = map[string]onboard.TelegramGroupRule{
					"-100": {
						Enabled:        &enabled,
						AllowFrom:      []int64{42, 43},
						RequireMention: &requireMention,
					},
				}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	tg := firstTelegram(t, cfg)
	assert.Equal(t, "allowlist", tg.GroupPolicy)
	assert.Equal(t, []int64{-100, -200}, tg.GroupAllowFrom)
	require.NotNil(t, tg.Groups)
	rule, ok := tg.Groups[-100]
	require.True(t, ok)
	require.NotNil(t, rule.Enabled)
	assert.True(t, *rule.Enabled)
	assert.Equal(t, []int64{42, 43}, rule.AllowFrom)
	require.NotNil(t, rule.RequireMention)
	assert.False(t, *rule.RequireMention)
}

func TestLoad_TelegramGroupsInvalidChatID(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.Groups = map[string]onboard.TelegramGroupRule{
					"not-a-number": {},
				}
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid group chat_id")
}

func TestLoad_TelegramRequireMention(t *testing.T) {
	rm := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.RequireMention = &rm
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	tg := firstTelegram(t, cfg)
	require.NotNil(t, tg.RequireMention)
	assert.True(t, *tg.RequireMention)
}

func TestLoad_TelegramStreamingFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.Streaming = "off"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "off", firstTelegram(t, cfg).Streaming)
}

func TestLoad_TelegramStreamingInvalid(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.Streaming = "full"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid streaming")
}

func TestLoad_TelegramChunkModeFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.ChunkMode = "newline"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "newline", firstTelegram(t, cfg).ChunkMode)
}

func TestLoad_TelegramChunkModeInvalid(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.ChunkMode = "paragraph"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid chunk_mode")
}

func TestLoad_TelegramTextChunkLimitFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.TextChunkLimit = 2000
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 2000, firstTelegram(t, cfg).TextChunkLimit)
}

func TestLoad_TelegramTextChunkLimitEnv(t *testing.T) {
	t.Setenv("TELEGRAM_TEXT_CHUNK_LIMIT", "500")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 500, firstTelegram(t, cfg).TextChunkLimit)
}

func TestLoad_TelegramTextChunkLimitEnvInvalid(t *testing.T) {
	t.Setenv("TELEGRAM_TEXT_CHUNK_LIMIT", "50") // below minimum of 100
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "TELEGRAM_TEXT_CHUNK_LIMIT")
}

func TestLoad_TelegramGroupPolicyInvalid(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.GroupPolicy = "invalid"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid group_policy")
}

func TestLoad_TelegramGroupAllowFromEnv(t *testing.T) {
	t.Setenv("TELEGRAM_GROUP_ALLOW_FROM", "-100,-200")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, []int64{-100, -200}, firstTelegram(t, cfg).GroupAllowFrom)
}

func TestLoad_TelegramGroupAllowFromEnvInvalid(t *testing.T) {
	t.Setenv("TELEGRAM_GROUP_ALLOW_FROM", "not-a-number")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "TELEGRAM_GROUP_ALLOW_FROM")
}

func TestLoad_WeComWebSocket_Basic(t *testing.T) {
	t.Setenv("WC_BOT", "bot-ws-id")
	t.Setenv("WC_SEC", "secret-ws")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.ConnectionMode = "websocket"
				ch.BotID = "${WC_BOT}"
				ch.Secret = "${WC_SEC}"
				ch.WSURL = "wss://custom.ws.example.com"
				ch.HeartbeatInt = "45s"
				ch.DMPolicy = "open"
				ch.GroupPolicy = "disabled"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.WeCom, 1)
	wc := cfg.WeCom[0]
	assert.Equal(t, "websocket", wc.ConnectionMode)
	assert.Equal(t, "bot-ws-id", wc.BotID)
	assert.Equal(t, "secret-ws", wc.Secret)
	assert.Equal(t, "wss://custom.ws.example.com", wc.WSURL)
	assert.Equal(t, 45*time.Second, wc.HeartbeatInterval)
	assert.Equal(t, "open", wc.DMPolicy)
	assert.Equal(t, "disabled", wc.GroupPolicy)
}

func TestLoad_WeComWebSocket_MissingBotID(t *testing.T) {
	t.Setenv("WC_SEC", "secret-ws")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.ConnectionMode = "websocket"
				ch.BotID = ""
				ch.Secret = "${WC_SEC}"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "websocket mode requires botid")
}

func TestLoad_WeComWebSocket_MissingSecret(t *testing.T) {
	t.Setenv("WC_BOT", "bot-id")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.ConnectionMode = "websocket"
				ch.BotID = "${WC_BOT}"
				ch.Secret = ""
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "websocket mode requires secret")
}

func TestLoad_WeComWebSocket_InvalidHeartbeat(t *testing.T) {
	t.Setenv("WC_BOT", "bot-id")
	t.Setenv("WC_SEC", "secret")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.ConnectionMode = "websocket"
				ch.BotID = "${WC_BOT}"
				ch.Secret = "${WC_SEC}"
				ch.HeartbeatInt = "not-a-duration"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid heartbeat_interval")
}

func TestLoad_WeComWebSocket_InvalidConnectionMode(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.ConnectionMode = "grpc"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid connection_mode")
}

func TestLoad_WeComGroups(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.Token = "tok"
				ch.EncodingAESKey = "key"
				ch.WebhookPath = "/wh"
				ch.GroupPolicy = "allowlist"
				ch.GroupAllowFrom = []string{"group-1"}
				allowEnabled := true
				ch.Groups = map[string]onboard.WeComGroupRuleFile{
					"group-1": {Enabled: &allowEnabled, AllowFrom: []string{"user-a"}},
				}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.WeCom, 1)
	wc := cfg.WeCom[0]
	assert.Equal(t, "allowlist", wc.GroupPolicy)
	assert.Equal(t, []string{"group-1"}, wc.GroupAllowFrom)
	require.NotNil(t, wc.Groups)
	rule, ok := wc.Groups["group-1"]
	require.True(t, ok)
	require.NotNil(t, rule.Enabled)
	assert.True(t, *rule.Enabled)
	assert.Equal(t, []string{"user-a"}, rule.AllowFrom)
}

func TestLoad_WeComDMPolicyInvalid(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.Token = "tok"
				ch.EncodingAESKey = "key"
				ch.WebhookPath = "/wh"
				ch.DMPolicy = "invalid"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid dm_policy")
}

func TestLoad_WeComGroupPolicyInvalid(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.Token = "tok"
				ch.EncodingAESKey = "key"
				ch.WebhookPath = "/wh"
				ch.GroupPolicy = "invalid"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid group_policy")
}

func TestLoad_WeComWebSocket_TokenAndAESKey(t *testing.T) {
	t.Setenv("WC_BOT", "bot-id")
	t.Setenv("WC_SEC", "secret")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.ConnectionMode = "websocket"
				ch.BotID = "${WC_BOT}"
				ch.Secret = "${WC_SEC}"
				ch.Token = "ws-token"
				ch.EncodingAESKey = "ws-aes-key"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.WeCom, 1)
	assert.Equal(t, "ws-token", cfg.WeCom[0].Token)
	assert.Equal(t, "ws-aes-key", cfg.WeCom[0].EncodingAESKey)
}

func TestLoad_TelegramSoulContent(t *testing.T) {
	dir := t.TempDir()
	clawdexDir := filepath.Join(dir, ".clawdex")
	require.NoError(t, os.MkdirAll(clawdexDir, 0o755))

	// Write global SOUL.md
	soulPath := filepath.Join(clawdexDir, "SOUL.md")
	require.NoError(t, os.WriteFile(soulPath, []byte("Be helpful."), 0o644))

	t.Setenv("HOME", dir)

	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	}
	path := filepath.Join(clawdexDir, "clawdex.json")
	require.NoError(t, onboard.SaveFileConfigTo(cfg, path))

	loadedCfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "Be helpful.", firstTelegram(t, loadedCfg).SoulContent)
	assert.Equal(t, "Be helpful.", loadedCfg.Codex.SoulContent)
}

func TestLoad_TelegramInstanceSoul(t *testing.T) {
	dir := t.TempDir()
	clawdexDir := filepath.Join(dir, ".clawdex")
	require.NoError(t, os.MkdirAll(clawdexDir, 0o755))

	// Write instance-specific SOUL
	require.NoError(t, os.WriteFile(filepath.Join(clawdexDir, "SOUL-my-tg.md"), []byte("Instance soul."), 0o644))

	t.Setenv("HOME", dir)

	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"my-tg": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	}
	path := filepath.Join(clawdexDir, "clawdex.json")
	require.NoError(t, onboard.SaveFileConfigTo(cfg, path))

	loadedCfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "Instance soul.", firstTelegram(t, loadedCfg).SoulContent)
}

// ── Additional coverage tests for WeCom, Weixin, QQBot validation ──

func TestLoad_WeComDuplicateWebhookPaths(t *testing.T) {
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc1": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = "tok1"
				ch.EncodingAESKey = "key1"
				ch.WebhookPath = "/wecom/hook"
			}),
			"wc2": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = "tok2"
				ch.EncodingAESKey = "key2"
				ch.WebhookPath = "/wecom/hook"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "duplicate wecom webhook path")
}

func TestLoad_WeComDuplicateNames(t *testing.T) {
	// With JSON map keys being unique, we can't truly create duplicate names via
	// the FileConfig. But we can verify two distinct WeCom channels load fine.
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc-a": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = "tok1"
				ch.EncodingAESKey = "key1"
				ch.WebhookPath = "/a"
			}),
			"wc-b": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = "tok2"
				ch.EncodingAESKey = "key2"
				ch.WebhookPath = "/b"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Len(t, cfg.WeCom, 2)
}

func TestLoad_WeComWebSocket_NegativeHeartbeat(t *testing.T) {
	t.Setenv("WC_BOT", "bot-id")
	t.Setenv("WC_SEC", "secret")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.ConnectionMode = "websocket"
				ch.BotID = "${WC_BOT}"
				ch.Secret = "${WC_SEC}"
				ch.HeartbeatInt = "-5s"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "invalid heartbeat_interval")
}

func TestLoad_WeComWebhook_MissingToken(t *testing.T) {
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = ""
				ch.EncodingAESKey = "key"
				ch.WebhookPath = "/wh"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "token is empty")
}

func TestLoad_WeComWebhook_MissingAESKey(t *testing.T) {
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = "tok"
				ch.EncodingAESKey = ""
				ch.WebhookPath = "/wh"
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "encoding_aes_key is empty")
}

func TestLoad_WeComWebhook_MissingWebhookPath(t *testing.T) {
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = "tok"
				ch.EncodingAESKey = "key"
				ch.WebhookPath = ""
			}),
		},
	})

	_, err := Load()
	assert.ErrorContains(t, err, "webhook_path is empty")
}

func TestLoad_WeComTextChunkLimit(t *testing.T) {
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = "tok"
				ch.EncodingAESKey = "key"
				ch.WebhookPath = "/wh"
				ch.TextChunkLimit = 2048
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.WeCom, 1)
	assert.Equal(t, 2048, cfg.WeCom[0].TextChunkLimit)
}

func TestLoad_WeixinAllowFrom(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": wxChannel(func(ch *onboard.WeixinChannelConfig) {
				ch.Token = "wxtoken"
				ch.AllowFrom = []string{"user1@im.wechat", "user2@im.wechat"}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.Weixin, 1)
	assert.Equal(t, []string{"user1@im.wechat", "user2@im.wechat"}, cfg.Weixin[0].AllowFrom)
}

func TestLoad_QQBotDuplicateNames(t *testing.T) {
	// Two QQ bot instances should load fine (JSON keys are unique).
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"qq-1": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app1"
				ch.ClientSecret = "sec1"
			}),
			"qq-2": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app2"
				ch.ClientSecret = "sec2"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Len(t, cfg.QQBot, 2)
}

func TestLoad_QQBotChannel_DMPolicyFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"qq": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app"
				ch.ClientSecret = "sec"
				ch.DMPolicy = "pairing"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.QQBot, 1)
	assert.Equal(t, "pairing", cfg.QQBot[0].DMPolicy)
}

func TestLoad_QQBotChannel_GroupPolicyFromFile(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"qq": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app"
				ch.ClientSecret = "sec"
				ch.GroupPolicy = "open"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.QQBot, 1)
	assert.Equal(t, "open", cfg.QQBot[0].GroupPolicy)
}

func TestLoad_WeComWebSocket_NoWebhookPathRequired(t *testing.T) {
	// WebSocket mode should NOT require webhook_path.
	t.Setenv("WC_BOT", "bot-ws")
	t.Setenv("WC_SEC", "sec-ws")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				enabled := true
				ch.Enabled = &enabled
				ch.ConnectionMode = "websocket"
				ch.BotID = "${WC_BOT}"
				ch.Secret = "${WC_SEC}"
				ch.WebhookPath = ""
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.WeCom, 1)
	assert.Equal(t, "websocket", cfg.WeCom[0].ConnectionMode)
}

func TestLoad_TelegramEnabledTrue(t *testing.T) {
	t.Setenv("TELEGRAM_ENABLED", "true")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, firstTelegram(t, cfg).Enabled)
}

func TestLoad_TelegramEnabled1(t *testing.T) {
	t.Setenv("TELEGRAM_ENABLED", "1")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.True(t, firstTelegram(t, cfg).Enabled)
}

func TestLoad_TelegramEnabled0(t *testing.T) {
	t.Setenv("TELEGRAM_ENABLED", "0")
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
			}),
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = "tok"
				ch.EncodingAESKey = "key"
				ch.WebhookPath = "/wh"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.False(t, firstTelegram(t, cfg).Enabled)
}

func TestLoad_TelegramStreamingEnvOverride(t *testing.T) {
	t.Setenv("TELEGRAM_STREAMING", "progress")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.Streaming = "off"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "progress", firstTelegram(t, cfg).Streaming)
}

func TestLoad_TelegramChunkModeEnvOverride(t *testing.T) {
	t.Setenv("TELEGRAM_CHUNK_MODE", "newline")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.ChunkMode = "length"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "newline", firstTelegram(t, cfg).ChunkMode)
}

func TestLoad_TelegramGroupPolicyEnvOverride(t *testing.T) {
	t.Setenv("TELEGRAM_GROUP_POLICY", "open")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.GroupPolicy = "disabled"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "open", firstTelegram(t, cfg).GroupPolicy)
}

func TestLoad_WeComAllowFrom(t *testing.T) {
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = "tok"
				ch.EncodingAESKey = "key"
				ch.WebhookPath = "/wh"
				ch.AllowFrom = []string{"user1", "user2"}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.WeCom, 1)
	assert.Equal(t, []string{"user1", "user2"}, cfg.WeCom[0].AllowFrom)
}

func TestLoad_WeComGroupAllowFrom(t *testing.T) {
	tr := true
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": wcChannel(func(ch *onboard.WeComChannelConfig) {
				ch.Enabled = &tr
				ch.Token = "tok"
				ch.EncodingAESKey = "key"
				ch.WebhookPath = "/wh"
				ch.GroupPolicy = "allowlist"
				ch.GroupAllowFrom = []string{"grp1", "grp2"}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.WeCom, 1)
	assert.Equal(t, []string{"grp1", "grp2"}, cfg.WeCom[0].GroupAllowFrom)
}

func TestLoad_WeixinDMPolicyAllowlist(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": wxChannel(func(ch *onboard.WeixinChannelConfig) {
				ch.Token = "wxtoken"
				ch.DMPolicy = "allowlist"
				ch.AllowFrom = []string{"user1@im.wechat"}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.Weixin, 1)
	assert.Equal(t, "allowlist", cfg.Weixin[0].DMPolicy)
}

func TestLoad_QQBotChannel_AllowFrom(t *testing.T) {
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"qq": qqChannel(func(ch *onboard.QQBotChannelConfig) {
				ch.AppID = "app"
				ch.ClientSecret = "sec"
				ch.AllowFrom = []string{"openid1", "openid2"}
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	require.Len(t, cfg.QQBot, 1)
	assert.Equal(t, []string{"openid1", "openid2"}, cfg.QQBot[0].AllowFrom)
}

func TestLoad_AllowlistWithEnvUsers(t *testing.T) {
	t.Setenv("TELEGRAM_ALLOW_FROM", "500")
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:token"
				ch.DMPolicy = "allowlist"
			}),
		},
	})

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "allowlist", firstTelegram(t, cfg).DMPolicy)
	assert.Equal(t, []int64{500}, firstTelegram(t, cfg).AllowFrom)
}
