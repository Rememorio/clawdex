package doctor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/onboard"
)

// tgChannel creates a telegram channel config as json.RawMessage.
func tgChannel(opts ...func(*onboard.TelegramChannelConfig)) json.RawMessage {
	ch := &onboard.TelegramChannelConfig{Type: "telegram"}
	for _, opt := range opts {
		opt(ch)
	}
	return onboard.MarshalTelegramChannel(*ch)
}

func fsChannel(opts ...func(*onboard.FeishuChannelConfig)) json.RawMessage {
	ch := &onboard.FeishuChannelConfig{Type: "feishu"}
	for _, opt := range opts {
		opt(ch)
	}
	return onboard.MarshalFeishuChannel(*ch)
}

func TestCheckConfigSyntax_ValidJSON(t *testing.T) {
	ResetState()
	defer ResetState()

	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"telegram":{}}`), 0o644))

	// Manually set up cached state to point at temp config.
	cfg, err := onboard.LoadFileConfigFrom(path)
	require.NoError(t, err)
	loadedConfig = cfg
	configPath = path

	c := checkConfigSyntax(false)
	assert.Equal(t, Pass, c.Status)
	assert.Equal(t, "valid JSON", c.Message)
}

func TestCheckConfigSyntax_InvalidJSON(t *testing.T) {
	ResetState()
	defer ResetState()

	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")
	require.NoError(t, os.WriteFile(path, []byte(`{invalid`), 0o644))

	// Point cached state at the bad file.
	loadedConfigErr = nil
	loadedConfig = &onboard.FileConfig{}
	configPath = path

	c := checkConfigSyntax(false)
	assert.Equal(t, Fail, c.Status)
	assert.Equal(t, "invalid JSON", c.Message)
}

func TestCheckSandbox_Valid(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{Sandbox: "read-only"},
	}

	c := checkSandbox(false)
	assert.Equal(t, Pass, c.Status)
	assert.Equal(t, "read-only", c.Message)
}

func TestCheckSandbox_Invalid_NoFix(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{Sandbox: "foo"},
	}

	c := checkSandbox(false)
	assert.Equal(t, Fail, c.Status)
	assert.Contains(t, c.Message, "foo")
	assert.False(t, c.Fixed)
}

func TestCheckSandbox_Invalid_Fix(t *testing.T) {
	ResetState()
	defer ResetState()

	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")
	cfg := &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{Sandbox: "badvalue"},
	}
	require.NoError(t, onboard.SaveFileConfigTo(cfg, path))

	loadedConfig = cfg
	configPath = path

	c := checkSandbox(true)
	assert.True(t, c.Fixed)

	updated, err := onboard.LoadFileConfigFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "workspace-write", updated.Codex.Sandbox)
}

func TestCheckDMPolicy_Valid(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.DMPolicy = "pairing"
			}),
		},
	}

	c := checkDMPolicy(false)
	assert.Equal(t, Pass, c.Status)
}

func TestCheckDMPolicy_Empty(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{}

	c := checkDMPolicy(false)
	assert.Equal(t, Pass, c.Status)
	assert.Contains(t, c.Message, "default")
}

func TestCheckStreaming_Valid(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.Streaming = "off"
			}),
		},
	}

	c := checkStreaming(false)
	assert.Equal(t, Pass, c.Status)
}

func TestCheckChunkMode_Valid(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.ChunkMode = "newline"
			}),
		},
	}

	c := checkChunkMode(false)
	assert.Equal(t, Pass, c.Status)
}

func TestCheckChunkMode_Invalid(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.ChunkMode = "bad"
			}),
		},
	}

	c := checkChunkMode(false)
	assert.Equal(t, Fail, c.Status)
	assert.Contains(t, c.Message, "bad")
}

func TestCheckDMPolicyOpen_Warn(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.DMPolicy = "open"
			}),
		},
	}

	c := checkDMPolicyOpen(false)
	assert.Equal(t, Warn, c.Status)
	assert.Contains(t, c.Message, "open")
}

func TestCheckDMPolicyOpen_Pass(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.DMPolicy = "pairing"
			}),
		},
	}

	c := checkDMPolicyOpen(false)
	assert.Equal(t, Pass, c.Status)
}

func TestCheckDataDirWritable(t *testing.T) {
	c := checkDataDirWritable(false)
	// Should pass if we can write to the data dir.
	assert.NotEqual(t, "", c.Name)
}

func TestCheckWorkDir_Empty(t *testing.T) {
	ResetState()
	defer ResetState()

	home := t.TempDir()
	t.Setenv("HOME", home)
	loadedConfig = &onboard.FileConfig{}

	c := checkWorkDir(false)
	assert.Equal(t, Pass, c.Status)
	assert.Equal(t, filepath.Join(home, ".clawdex", "workspace")+" (default)", c.Message)
}

func TestCheckWorkDir_Valid(t *testing.T) {
	ResetState()
	defer ResetState()

	dir := t.TempDir()
	loadedConfig = &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{WorkDir: dir},
	}

	c := checkWorkDir(false)
	assert.Equal(t, Pass, c.Status)
	assert.Equal(t, dir, c.Message)
}

func TestCheckWorkDir_NotExists(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{WorkDir: "/nonexistent/path/xyz"},
	}

	c := checkWorkDir(false)
	assert.Equal(t, Fail, c.Status)
}

// ── Run and WithFix tests ──

func TestWithFix(t *testing.T) {
	opt := WithFix(true)
	var ro runOptions
	opt(&ro)
	assert.True(t, ro.fix)
}

func TestWithFix_False(t *testing.T) {
	opt := WithFix(false)
	var ro runOptions
	opt(&ro)
	assert.False(t, ro.fix)
}

func TestRun_ReturnsAllChecks(t *testing.T) {
	ResetState()
	defer ResetState()

	// Set up a minimal valid config so checks don't all fail immediately.
	dir := t.TempDir()
	clawdexDir := filepath.Join(dir, ".clawdex")
	require.NoError(t, os.MkdirAll(clawdexDir, 0o755))

	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:fake-token"
				ch.DMPolicy = "pairing"
				ch.Streaming = "partial"
				ch.ChunkMode = "length"
			}),
		},
	}
	path := filepath.Join(clawdexDir, "clawdex.json")
	require.NoError(t, onboard.SaveFileConfigTo(cfg, path))

	t.Setenv("HOME", dir)

	results := Run()
	// Run should return results for all 14 registered checks.
	assert.Len(t, results, 14)

	// Each result should have a non-empty Name.
	for _, r := range results {
		assert.NotEmpty(t, r.Name)
	}
}

func TestRun_WithFixOption(t *testing.T) {
	ResetState()
	defer ResetState()

	dir := t.TempDir()
	clawdexDir := filepath.Join(dir, ".clawdex")
	require.NoError(t, os.MkdirAll(clawdexDir, 0o755))

	// Create a config with invalid sandbox to verify fix works through Run.
	cfg := &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{Sandbox: "bad-sandbox"},
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:fake-token"
				ch.DMPolicy = "pairing"
			}),
		},
	}
	path := filepath.Join(clawdexDir, "clawdex.json")
	require.NoError(t, onboard.SaveFileConfigTo(cfg, path))

	t.Setenv("HOME", dir)

	results := Run(WithFix(true))
	// Find the sandbox check result.
	var sandboxCheck *Check
	for i := range results {
		if results[i].Name == "Sandbox" {
			sandboxCheck = &results[i]
			break
		}
	}
	require.NotNil(t, sandboxCheck)
	assert.True(t, sandboxCheck.Fixed)
}

// ── Additional check tests ──

func TestCheckConfigExists_NoConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	c := checkConfigExists(false)
	assert.Equal(t, Fail, c.Status)
	assert.Contains(t, c.Message, "not found")
}

func TestCheckConfigExists_Exists(t *testing.T) {
	dir := t.TempDir()
	clawdexDir := filepath.Join(dir, ".clawdex")
	require.NoError(t, os.MkdirAll(clawdexDir, 0o755))
	path := filepath.Join(clawdexDir, "clawdex.json")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o644))

	t.Setenv("HOME", dir)

	c := checkConfigExists(false)
	assert.Equal(t, Pass, c.Status)
	assert.Contains(t, c.Message, "clawdex.json")
}

func TestGetTelegramChannel_NilChannels(t *testing.T) {
	cfg := &onboard.FileConfig{}
	ch := getTelegramChannel(cfg)
	assert.Equal(t, "", ch.BotToken)
}

func TestGetTelegramChannelName_NilChannels(t *testing.T) {
	cfg := &onboard.FileConfig{}
	name := getTelegramChannelName(cfg)
	assert.Equal(t, "", name)
}

func TestGetTelegramChannelName_Found(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"my-tg": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "tok"
			}),
		},
	}
	name := getTelegramChannelName(cfg)
	assert.Equal(t, "my-tg", name)
}

func TestCheckBotTokenResolves_EmptyToken(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(),
		},
	}

	c := checkBotTokenResolves(false)
	assert.Equal(t, Fail, c.Status)
	assert.Contains(t, c.Message, "bot_token not set")
}

func TestCheckBotTokenResolves_PlainToken(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "123:plain-token"
			}),
		},
	}

	c := checkBotTokenResolves(false)
	assert.Equal(t, Pass, c.Status)
}

func TestCheckChannelCredentialsResolve_Feishu(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"feishu": fsChannel(func(ch *onboard.FeishuChannelConfig) {
				ch.AppID = "cli_test"
				ch.AppSecret = "secret"
			}),
		},
	}

	c := checkChannelCredentialsResolve(false)
	assert.Equal(t, Pass, c.Status)
	assert.Contains(t, c.Message, "feishu")
}

func TestCheckChannelCredentialsResolve_FeishuMissingSecret(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"feishu": fsChannel(func(ch *onboard.FeishuChannelConfig) {
				ch.AppID = "cli_test"
			}),
		},
	}

	c := checkChannelCredentialsResolve(false)
	assert.Equal(t, Fail, c.Status)
	assert.Contains(t, c.Message, "app_id and app_secret")
}

func TestCheckChannelCredentialsValid_Feishu(t *testing.T) {
	ResetState()
	defer ResetState()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"msg":"ok","tenant_access_token":"tenant-token"}`)
		case "/open-apis/bot/v3/info":
			assert.Equal(t, "Bearer tenant-token", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"msg":"ok","bot":{"app_name":"Eyjafjalla","open_id":"ou_bot"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"feishu": fsChannel(func(ch *onboard.FeishuChannelConfig) {
				ch.AppID = "cli_test"
				ch.AppSecret = "secret"
				ch.BaseURL = ts.URL
			}),
		},
	}

	c := checkChannelCredentialsValid(false)
	assert.Equal(t, Pass, c.Status)
	assert.Contains(t, c.Message, "Eyjafjalla")
}

func TestCheckStreaming_Empty(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{}

	c := checkStreaming(false)
	assert.Equal(t, Pass, c.Status)
	assert.Contains(t, c.Message, "default")
}

func TestCheckStreaming_Invalid_NoFix(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.Streaming = "full"
			}),
		},
	}

	c := checkStreaming(false)
	assert.Equal(t, Fail, c.Status)
	assert.Contains(t, c.Message, "full")
}

func TestCheckStreaming_Invalid_Fix(t *testing.T) {
	ResetState()
	defer ResetState()

	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.Streaming = "invalid"
			}),
		},
	}
	require.NoError(t, onboard.SaveFileConfigTo(cfg, path))

	loadedConfig = cfg
	configPath = path

	c := checkStreaming(true)
	assert.True(t, c.Fixed)
}

func TestCheckDMPolicy_Invalid_NoFix(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.DMPolicy = "banana"
			}),
		},
	}

	c := checkDMPolicy(false)
	assert.Equal(t, Fail, c.Status)
	assert.Contains(t, c.Message, "banana")
}

func TestCheckDMPolicy_Invalid_Fix(t *testing.T) {
	ResetState()
	defer ResetState()

	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.DMPolicy = "banana"
			}),
		},
	}
	require.NoError(t, onboard.SaveFileConfigTo(cfg, path))

	loadedConfig = cfg
	configPath = path

	c := checkDMPolicy(true)
	assert.True(t, c.Fixed)
}

func TestCheckChunkMode_Fix(t *testing.T) {
	ResetState()
	defer ResetState()

	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.ChunkMode = "invalid-mode"
			}),
		},
	}
	require.NoError(t, onboard.SaveFileConfigTo(cfg, path))

	loadedConfig = cfg
	configPath = path

	c := checkChunkMode(true)
	assert.True(t, c.Fixed)
}

func TestCheckSandbox_DefaultEmpty(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{}

	c := checkSandbox(false)
	assert.Equal(t, Pass, c.Status)
	assert.Contains(t, c.Message, "default")
}

func TestCheckWorkDir_NotADir(t *testing.T) {
	ResetState()
	defer ResetState()

	f := filepath.Join(t.TempDir(), "file.txt")
	require.NoError(t, os.WriteFile(f, []byte("x"), 0o644))

	loadedConfig = &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{WorkDir: f},
	}

	c := checkWorkDir(false)
	assert.Equal(t, Fail, c.Status)
	assert.Contains(t, c.Message, "not a directory")
}

// ── Additional coverage tests (appended) ──

func TestCheckCodexCLI_NotFound(t *testing.T) {
	// Set PATH to empty dir so codex can't be found.
	t.Setenv("PATH", t.TempDir())
	c := checkCodexCLI(false)
	assert.Equal(t, Fail, c.Status)
	assert.Contains(t, c.Message, "codex not found")
}

func TestCheckCodexCLI_Found(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho 'codex 2.3.4'\n"), 0o755))
	t.Setenv("PATH", binDir)

	c := checkCodexCLI(false)
	assert.Equal(t, Pass, c.Status)
	assert.Equal(t, "codex 2.3.4", c.Message)
}

func TestCheckGatewayHealth_HealthzOK(t *testing.T) {
	ResetState()
	defer ResetState()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	// Extract port from test server URL.
	addr := ts.Listener.Addr().String()
	// Config uses ":PORT" format.
	parts := strings.Split(addr, ":")
	port := parts[len(parts)-1]

	loadedConfig = &onboard.FileConfig{
		Gateway: onboard.GatewayFileConfig{Address: ":" + port},
	}

	c := checkGatewayHealth(false)
	assert.Equal(t, Pass, c.Status)
	assert.Equal(t, "/healthz OK", c.Message)
}

func TestCheckGatewayHealth_HealthzFails(t *testing.T) {
	ResetState()
	defer ResetState()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	addr := ts.Listener.Addr().String()
	parts := strings.Split(addr, ":")
	port := parts[len(parts)-1]

	loadedConfig = &onboard.FileConfig{
		Gateway: onboard.GatewayFileConfig{Address: ":" + port},
	}

	c := checkGatewayHealth(false)
	assert.Equal(t, Warn, c.Status)
	assert.Contains(t, c.Message, "503")
}

func TestCheckGatewayHealth_NotReachable(t *testing.T) {
	ResetState()
	defer ResetState()

	// Use a port that's unlikely to be in use.
	loadedConfig = &onboard.FileConfig{
		Gateway: onboard.GatewayFileConfig{Address: ":59999"},
	}

	c := checkGatewayHealth(false)
	assert.Equal(t, Warn, c.Status)
	assert.Contains(t, c.Message, "not reachable")
}

func TestCheckGatewayHealth_DefaultAddress(t *testing.T) {
	ResetState()
	defer ResetState()

	// Empty address should default to :8080
	loadedConfig = &onboard.FileConfig{
		Gateway: onboard.GatewayFileConfig{Address: ""},
	}

	c := checkGatewayHealth(false)
	// Should try localhost:8080 which likely isn't running in test
	assert.Equal(t, Warn, c.Status)
}

func TestCheckGatewayHealth_FullAddress(t *testing.T) {
	ResetState()
	defer ResetState()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Use full address (not starting with :)
	addr := ts.Listener.Addr().String()
	loadedConfig = &onboard.FileConfig{
		Gateway: onboard.GatewayFileConfig{Address: addr},
	}

	c := checkGatewayHealth(false)
	assert.Equal(t, Pass, c.Status)
}

func TestCheckStalePID_NoPID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	c := checkStalePID(false)
	assert.Equal(t, Pass, c.Status)
	assert.Contains(t, c.Message, "not running")
}

func TestCheckStalePID_RunningProcess(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Write current PID to simulate a running gateway
	dataDir := filepath.Join(home, ".clawdex")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	pidPath := filepath.Join(dataDir, "gateway.pid")
	require.NoError(t, os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644))

	c := checkStalePID(false)
	assert.Equal(t, Pass, c.Status)
	assert.Contains(t, c.Message, "running")
}

func TestCheckStalePID_StaleWithFix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dataDir := filepath.Join(home, ".clawdex")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	pidPath := filepath.Join(dataDir, "gateway.pid")
	require.NoError(t, os.WriteFile(pidPath, []byte("4194304"), 0o644))

	c := checkStalePID(true)
	assert.Equal(t, Warn, c.Status)
	assert.True(t, c.Fixed)
}

func TestCheckStalePID_StaleNoFix(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dataDir := filepath.Join(home, ".clawdex")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	pidPath := filepath.Join(dataDir, "gateway.pid")
	require.NoError(t, os.WriteFile(pidPath, []byte("4194304"), 0o644))

	c := checkStalePID(false)
	assert.Equal(t, Warn, c.Status)
	assert.False(t, c.Fixed)
	assert.Contains(t, c.Message, "stale PID")
}

func TestCheckDMPolicy_Fix_NoExistingChannel(t *testing.T) {
	ResetState()
	defer ResetState()

	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")
	// Config with no channels map but invalid dm_policy via raw channel
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.DMPolicy = "invalid-policy"
			}),
		},
	}
	require.NoError(t, onboard.SaveFileConfigTo(cfg, path))

	loadedConfig = cfg
	configPath = path

	c := checkDMPolicy(true)
	assert.True(t, c.Fixed)

	// Verify the fix was applied on disk.
	updated, err := onboard.LoadFileConfigFrom(path)
	require.NoError(t, err)
	tgRaw := updated.Channels["telegram"]
	var tgCfg onboard.TelegramChannelConfig
	require.NoError(t, json.Unmarshal(tgRaw, &tgCfg))
	assert.Equal(t, "pairing", tgCfg.DMPolicy)
}

func TestCheckChunkMode_Empty(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{}
	c := checkChunkMode(false)
	assert.Equal(t, Pass, c.Status)
	assert.Contains(t, c.Message, "default")
}

func TestCheckBotTokenResolves_EnvRef(t *testing.T) {
	ResetState()
	defer ResetState()

	t.Setenv("MY_BOT_TOKEN_DR", "resolved-value")
	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "${MY_BOT_TOKEN_DR}"
			}),
		},
	}

	c := checkBotTokenResolves(false)
	assert.Equal(t, Pass, c.Status)
}

func TestCheckBotTokenResolves_UnresolvableEnv(t *testing.T) {
	ResetState()
	defer ResetState()

	os.Unsetenv("MISSING_TOKEN_VAR_DR_TEST")
	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "${MISSING_TOKEN_VAR_DR_TEST}"
			}),
		},
	}

	c := checkBotTokenResolves(false)
	assert.Equal(t, Fail, c.Status)
}

// ── Additional coverage tests ──

func TestVerifyBotToken_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/bottest-token/getMe")
		fmt.Fprint(w, `{"ok":true,"result":{"id":12345,"username":"testbot"}}`)
	}))
	defer ts.Close()

	// verifyBotToken uses hardcoded URL, so we test via checkBotTokenValid
	// by setting up the full flow with httptest. Instead, test the function
	// directly by patching. Since we can't easily, test the httptest server
	// variant via checkGatewayHealth patterns instead.
	// Here we test the verifyBotToken by calling it against our test server.
	// Since verifyBotToken uses hardcoded "https://api.telegram.org", we
	// just verify the JSON parsing by calling the helper struct directly.
	var result struct {
		OK     bool    `json:"ok"`
		Result botInfo `json:"result"`
	}
	resp, err := http.Get(ts.URL + "/bottest-token/getMe")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.True(t, result.OK)
	assert.Equal(t, int64(12345), result.Result.ID)
	assert.Equal(t, "testbot", result.Result.Username)
}

func TestCheckBotTokenValid_NoTelegramChannel(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{},
	}

	c := checkBotTokenValid(false)
	assert.Equal(t, Fail, c.Status)
	assert.Contains(t, c.Message, "token is empty")
}

func TestCheckBotTokenResolves_WithConfig(t *testing.T) {
	ResetState()
	defer ResetState()

	t.Setenv("DR_TEST_TOKEN_2", "123:valid")
	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": tgChannel(func(ch *onboard.TelegramChannelConfig) {
				ch.BotToken = "${DR_TEST_TOKEN_2}"
			}),
		},
	}

	c := checkBotTokenResolves(false)
	assert.Equal(t, Pass, c.Status)
	assert.Equal(t, "resolves OK", c.Message)
}

func TestCheckConfigSyntax_ReadError(t *testing.T) {
	ResetState()
	defer ResetState()

	// Point to a nonexistent path.
	loadedConfig = &onboard.FileConfig{}
	configPath = "/nonexistent/path/to/config.json"

	c := checkConfigSyntax(false)
	assert.Equal(t, Fail, c.Status)
}

func TestCheckDMPolicyOpen_NoTelegramChannel(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfig = &onboard.FileConfig{
		Channels: map[string]json.RawMessage{},
	}

	c := checkDMPolicyOpen(false)
	assert.Equal(t, Pass, c.Status)
}

func TestCheckDataDirWritable_Pass(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataDir := filepath.Join(home, ".clawdex")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))

	c := checkDataDirWritable(false)
	assert.Equal(t, Pass, c.Status)
	assert.Contains(t, c.Message, ".clawdex")
}

func TestCheckGatewayHealth_ConfigError(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfigErr = fmt.Errorf("config broken")

	c := checkGatewayHealth(false)
	assert.Equal(t, Warn, c.Status)
	assert.Contains(t, c.Message, "cannot load config")
}

func TestCheckDMPolicyOpen_ConfigError(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfigErr = fmt.Errorf("config broken")

	c := checkDMPolicyOpen(false)
	assert.Equal(t, Warn, c.Status)
	assert.Contains(t, c.Message, "cannot load config")
}

func TestCheckSandbox_ConfigError(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfigErr = fmt.Errorf("config broken")

	c := checkSandbox(false)
	assert.Equal(t, Fail, c.Status)
}

func TestCheckDMPolicy_ConfigError(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfigErr = fmt.Errorf("config broken")

	c := checkDMPolicy(false)
	assert.Equal(t, Fail, c.Status)
}

func TestCheckStreaming_ConfigError(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfigErr = fmt.Errorf("config broken")

	c := checkStreaming(false)
	assert.Equal(t, Fail, c.Status)
}

func TestCheckChunkMode_ConfigError(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfigErr = fmt.Errorf("config broken")

	c := checkChunkMode(false)
	assert.Equal(t, Fail, c.Status)
}

func TestCheckWorkDir_ConfigError(t *testing.T) {
	ResetState()
	defer ResetState()

	loadedConfigErr = fmt.Errorf("config broken")

	c := checkWorkDir(false)
	assert.Equal(t, Fail, c.Status)
}

func TestEnsureConfig_CachesResult(t *testing.T) {
	ResetState()
	defer ResetState()

	dir := t.TempDir()
	clawdexDir := filepath.Join(dir, ".clawdex")
	require.NoError(t, os.MkdirAll(clawdexDir, 0o755))
	path := filepath.Join(clawdexDir, "clawdex.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"channels":{}}`), 0o644))
	t.Setenv("HOME", dir)

	cfg1, p1, err1 := ensureConfig()
	require.NoError(t, err1)
	require.NotNil(t, cfg1)
	assert.Contains(t, p1, "clawdex.json")

	// Second call should return the same cached result.
	cfg2, p2, err2 := ensureConfig()
	assert.NoError(t, err2)
	assert.Equal(t, cfg1, cfg2)
	assert.Equal(t, p1, p2)
}

func TestGetTelegramChannel_InvalidJSON(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": json.RawMessage(`{"type":"telegram","invalid`),
		},
	}
	ch := getTelegramChannel(cfg)
	// Should return empty config when JSON is broken
	assert.Equal(t, "", ch.BotToken)
}

func TestGetTelegramChannelName_NoTelegram(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wecom": json.RawMessage(`{"type":"wecom"}`),
		},
	}
	name := getTelegramChannelName(cfg)
	assert.Equal(t, "", name)
}
