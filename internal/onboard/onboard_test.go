package onboard

import (
	"bufio"
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
)

// simulateStdin replaces os.Stdin with a pipe containing the given input,
// restoring the original stdin after the test.
func simulateStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	_, err = w.WriteString(input)
	require.NoError(t, err)
	w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin; r.Close() })
}

// ── FileConfig round-trip tests ──

func TestFileConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	tTrue := true
	tg := TelegramChannelConfig{
		Type:           "telegram",
		BotToken:       "${TELEGRAM_BOT_TOKEN}",
		Enabled:        &tTrue,
		AllowFrom:      []int64{123456},
		DMPolicy:       "pairing",
		Streaming:      "partial",
		ChunkMode:      "length",
		TextChunkLimit: 3500,
	}
	cfg := &FileConfig{
		Codex: CodexFileConfig{
			WorkDir: "/home/user/project",
			Timeout: "30m",
		},
		Gateway: GatewayFileConfig{
			Address: ":9090",
		},
		Channels: map[string]json.RawMessage{
			"telegram": MarshalTelegramChannel(tg),
		},
	}

	require.NoError(t, SaveFileConfigTo(cfg, path))

	loaded, err := LoadFileConfigFrom(path)
	require.NoError(t, err)

	assert.Equal(t, cfg.Codex.WorkDir, loaded.Codex.WorkDir)
	assert.Equal(t, cfg.Codex.Timeout, loaded.Codex.Timeout)
	assert.Equal(t, cfg.Codex.MaxOutputRunes, loaded.Codex.MaxOutputRunes)
	assert.Equal(t, cfg.Gateway.Address, loaded.Gateway.Address)
	ch := MustParseTelegramChannel(loaded.Channels["telegram"])
	assert.Equal(t, "${TELEGRAM_BOT_TOKEN}", ch.BotToken)
	assert.Equal(t, []int64{123456}, ch.AllowFrom)
}

func TestFileConfigRoundTrip_Minimal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": MarshalTelegramChannel(TelegramChannelConfig{
				Type:     "telegram",
				BotToken: "file:///run/secrets/tg-token",
			}),
		},
	}

	require.NoError(t, SaveFileConfigTo(cfg, path))

	loaded, err := LoadFileConfigFrom(path)
	require.NoError(t, err)

	ch := MustParseTelegramChannel(loaded.Channels["telegram"])
	assert.Equal(t, "file:///run/secrets/tg-token", ch.BotToken)
	assert.Equal(t, "pairing", ch.DMPolicy)
	assert.Equal(t, "allowlist", ch.GroupPolicy)
	require.NotNil(t, ch.RequireMention)
	assert.True(t, *ch.RequireMention)
	assert.Equal(t, []int64{}, ch.AllowFrom)
	assert.Equal(t, []int64{}, ch.GroupAllowFrom)
	assert.Equal(t, "", loaded.Codex.WorkDir)
	assert.Equal(t, "", loaded.Codex.Timeout)
}

func TestLoadFileConfigFrom_NotExist(t *testing.T) {
	cfg, err := LoadFileConfigFrom("/nonexistent/path/clawdex.json")
	require.NoError(t, err)
	assert.Equal(t, &FileConfig{}, cfg)
}

func TestLoadFileConfigFrom_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(path, []byte("{invalid"), 0o644))

	_, err := LoadFileConfigFrom(path)
	assert.ErrorContains(t, err, "parse config file")
}

func TestLoadFileConfigFrom_ReadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")
	require.NoError(t, os.MkdirAll(path, 0o755))

	_, err := LoadFileConfigFrom(path)
	assert.Error(t, err)
}

func TestSaveFileConfigTo_JSONFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	cfg := &FileConfig{
		Codex: CodexFileConfig{
			WorkDir: "/tmp",
			Timeout: "10m",
		},
		Channels: map[string]json.RawMessage{
			"telegram": MarshalTelegramChannel(TelegramChannelConfig{
				Type:     "telegram",
				BotToken: "${MY_TOKEN}",
			}),
		},
	}

	require.NoError(t, SaveFileConfigTo(cfg, path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	content := string(data)
	assert.Contains(t, content, `"workdir": "/tmp"`)
	assert.Contains(t, content, `"timeout": "10m"`)
	assert.Contains(t, content, `"bot_token": "${MY_TOKEN}"`)
	assert.Contains(t, content, `"dm_policy": "pairing"`)
	assert.Contains(t, content, `"streaming": "partial"`)
	assert.Contains(t, content, `"group_policy": "allowlist"`)
	assert.Contains(t, content, `"require_mention": true`)
	assert.Contains(t, content, `"allow_from": []`)
	assert.Contains(t, content, `"group_allow_from": []`)
	assert.NotContains(t, content, `"allowed_chat_id"`)
	assert.NotContains(t, content, `"max_output_runes"`)
	assert.NotContains(t, content, `"address"`)
	assert.True(t, content[len(content)-1] == '\n')
}

func TestSaveFileConfigTo_WriteError(t *testing.T) {
	dir := t.TempDir()
	err := SaveFileConfigTo(&FileConfig{}, dir)
	assert.Error(t, err)
}

func TestFileConfig_OmitEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	cfg := &FileConfig{}
	require.NoError(t, SaveFileConfigTo(cfg, path))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	content := string(data)
	assert.NotContains(t, content, `"bot_token"`)
	assert.NotContains(t, content, `"workdir"`)
	assert.NotContains(t, content, `"timeout"`)
	assert.NotContains(t, content, `"max_output_runes"`)
}

func TestSaveFileConfigTo_RefuseEmptyOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	existing := &FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": MarshalTelegramChannel(TelegramChannelConfig{
				Type:     "telegram",
				BotToken: "test-token",
			}),
		},
	}
	require.NoError(t, SaveFileConfigTo(existing, path))

	err := SaveFileConfigTo(&FileConfig{}, path)
	require.ErrorIs(t, err, errRefuseEmptyConfigOverwrite)

	loaded, err := LoadFileConfigFrom(path)
	require.NoError(t, err)
	ch := MustParseTelegramChannel(loaded.Channels["telegram"])
	assert.Equal(t, "test-token", ch.BotToken)
}

func TestFileConfigRoundTrip_EnvRef(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": MarshalTelegramChannel(TelegramChannelConfig{
				Type:     "telegram",
				BotToken: "${TELEGRAM_BOT_TOKEN}",
			}),
		},
	}
	require.NoError(t, SaveFileConfigTo(cfg, path))
	loaded, err := LoadFileConfigFrom(path)
	require.NoError(t, err)
	ch := MustParseTelegramChannel(loaded.Channels["telegram"])
	assert.Equal(t, "${TELEGRAM_BOT_TOKEN}", ch.BotToken)
}

func TestFileConfigRoundTrip_FileRef(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": MarshalTelegramChannel(TelegramChannelConfig{
				Type:     "telegram",
				BotToken: "file:///etc/secrets/tg-token",
			}),
		},
	}
	require.NoError(t, SaveFileConfigTo(cfg, path))
	loaded, err := LoadFileConfigFrom(path)
	require.NoError(t, err)
	ch := MustParseTelegramChannel(loaded.Channels["telegram"])
	assert.Equal(t, "file:///etc/secrets/tg-token", ch.BotToken)
}

// ── ConfigPath / LoadFileConfig / SaveFileConfig with HOME override ──

func TestConfigPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	path, err := ConfigPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".clawdex", "clawdex.json"), path)
}

func TestLoadFileConfig_NotExist(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	assert.Equal(t, &FileConfig{}, cfg)
}

func TestSaveFileConfig_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"telegram": MarshalTelegramChannel(TelegramChannelConfig{
				Type:     "telegram",
				BotToken: "test-token",
			}),
		},
	}
	require.NoError(t, SaveFileConfig(cfg))

	loaded, err := LoadFileConfig()
	require.NoError(t, err)
	ch := MustParseTelegramChannel(loaded.Channels["telegram"])
	assert.Equal(t, "test-token", ch.BotToken)
}

// ── keepOption tests ──

func TestKeepOption_Empty(t *testing.T) {
	assert.Equal(t, "", keepOption(""))
}

func TestKeepOption_NonEmpty(t *testing.T) {
	assert.Equal(t, "/4", keepOption("some-token"))
}

// ── prompt helper tests ──

func newReader(input string) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(input))
}

func TestPrompt_DefaultValue(t *testing.T) {
	r := newReader("\n")
	val, err := prompt(r, "Test", "default-val")
	require.NoError(t, err)
	assert.Equal(t, "default-val", val)
}

func TestPrompt_CustomValue(t *testing.T) {
	r := newReader("custom\n")
	val, err := prompt(r, "Test", "default-val")
	require.NoError(t, err)
	assert.Equal(t, "custom", val)
}

func TestPrompt_NoDefault(t *testing.T) {
	r := newReader("input\n")
	val, err := prompt(r, "Test", "")
	require.NoError(t, err)
	assert.Equal(t, "input", val)
}

func TestPrompt_EmptyNoDefault(t *testing.T) {
	r := newReader("\n")
	val, err := prompt(r, "Test", "")
	require.NoError(t, err)
	assert.Equal(t, "", val)
}

// ── promptChoice tests ──

func TestPromptChoice_ValidChoice(t *testing.T) {
	r := newReader("2\n")
	choice, err := promptChoice(r, "Pick", "1", map[string]bool{"1": true, "2": true})
	require.NoError(t, err)
	assert.Equal(t, "2", choice)
}

func TestPromptChoice_DefaultChoice(t *testing.T) {
	r := newReader("\n")
	choice, err := promptChoice(r, "Pick", "1", map[string]bool{"1": true, "2": true})
	require.NoError(t, err)
	assert.Equal(t, "1", choice)
}

func TestPromptChoice_InvalidThenValid(t *testing.T) {
	// First invalid, then valid.
	r := newReader("9\n2\n")
	choice, err := promptChoice(r, "Pick", "1", map[string]bool{"1": true, "2": true})
	require.NoError(t, err)
	assert.Equal(t, "2", choice)
}

func TestPromptChoice_MultipleInvalidThenValid(t *testing.T) {
	// Multiple invalid, then valid.
	r := newReader("x\ny\nz\n1\n")
	choice, err := promptChoice(r, "Pick", "1", map[string]bool{"1": true})
	require.NoError(t, err)
	assert.Equal(t, "1", choice)
}

// ── probeCodex tests ──

func TestProbeCodex_NotFound(t *testing.T) {
	// Empty PATH so codex can't be found.
	t.Setenv("PATH", t.TempDir())
	err := probeCodex()
	assert.ErrorContains(t, err, "codex CLI not found")
}

func TestProbeCodex_FakeCodex(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho 'codex 1.2.3'\n"), 0o755))
	t.Setenv("PATH", binDir)

	err := probeCodex()
	assert.NoError(t, err)
}

// ── verifyBotToken tests ──

func TestVerifyBotToken_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/bottest-token/getMe", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"id": 123, "username": "testbot"},
		})
	}))
	defer ts.Close()

	// Temporarily override the function to use our test server.
	// Since verifyBotToken uses a hardcoded URL, we test it via
	// a helper that accepts a base URL.
	bot, err := verifyBotTokenWithURL(ts.URL+"/bot", "test-token")
	require.NoError(t, err)
	assert.Equal(t, int64(123), bot.ID)
	assert.Equal(t, "testbot", bot.Username)
}

func TestVerifyBotToken_Unauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"description": "Unauthorized",
		})
	}))
	defer ts.Close()

	_, err := verifyBotTokenWithURL(ts.URL+"/bot", "bad-token")
	assert.ErrorContains(t, err, "telegram rejected the token")
}

func TestVerifyBotToken_BadJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "not json")
	}))
	defer ts.Close()

	_, err := verifyBotTokenWithURL(ts.URL+"/bot", "tok")
	assert.ErrorContains(t, err, "parse telegram response")
}

func TestVerifyBotToken_EmptyDescription(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false})
	}))
	defer ts.Close()

	_, err := verifyBotTokenWithURL(ts.URL+"/bot", "tok")
	assert.ErrorContains(t, err, "unknown error")
}

// ── promptBotToken tests ──

func TestPromptBotToken_EnvRef(t *testing.T) {
	r := newReader("\n\n")
	val, err := promptBotToken(r, "")
	require.NoError(t, err)
	assert.Equal(t, "${TELEGRAM_BOT_TOKEN}", val)
}

func TestPromptBotToken_EnvRefCustom(t *testing.T) {
	r := newReader("1\nMY_VAR\n")
	val, err := promptBotToken(r, "")
	require.NoError(t, err)
	assert.Equal(t, "${MY_VAR}", val)
}

func TestPromptBotToken_Plaintext(t *testing.T) {
	r := newReader("2\n123456:ABC-DEF\n")
	val, err := promptBotToken(r, "")
	require.NoError(t, err)
	assert.Equal(t, "123456:ABC-DEF", val)
}

func TestPromptBotToken_PlaintextEmpty(t *testing.T) {
	// After empty input, loop continues; then provide valid plaintext.
	r := newReader("2\n\n2\nvalid-token\n")
	val, err := promptBotToken(r, "")
	require.NoError(t, err)
	assert.Equal(t, "valid-token", val)
}

func TestPromptBotToken_FileRef(t *testing.T) {
	r := newReader("3\n/run/secrets/token\n")
	val, err := promptBotToken(r, "")
	require.NoError(t, err)
	assert.Equal(t, "file:///run/secrets/token", val)
}

func TestPromptBotToken_FileRefEmpty(t *testing.T) {
	// After empty input, loop continues; then provide valid path.
	r := newReader("3\n\n3\n/run/secrets/token\n")
	val, err := promptBotToken(r, "")
	require.NoError(t, err)
	assert.Equal(t, "file:///run/secrets/token", val)
}

func TestPromptBotToken_FileRefRelative(t *testing.T) {
	r := newReader("3\nrelative/path/token\n")
	val, err := promptBotToken(r, "")
	require.NoError(t, err)
	assert.True(t, filepath.IsAbs(strings.TrimPrefix(val, "file://")))
}

func TestPromptBotToken_KeepCurrent(t *testing.T) {
	r := newReader("4\n")
	val, err := promptBotToken(r, "${EXISTING}")
	require.NoError(t, err)
	assert.Equal(t, "${EXISTING}", val)
}

func TestPromptBotToken_KeepCurrentEmpty(t *testing.T) {
	// After no current value, loop continues; then provide valid choice.
	r := newReader("4\n1\n\n")
	val, err := promptBotToken(r, "")
	require.NoError(t, err)
	assert.Equal(t, "${TELEGRAM_BOT_TOKEN}", val)
}

func TestPromptBotToken_InvalidChoice(t *testing.T) {
	// Invalid choice loops; then provide valid choice.
	r := newReader("9\n1\n\n")
	val, err := promptBotToken(r, "")
	require.NoError(t, err)
	assert.Equal(t, "${TELEGRAM_BOT_TOKEN}", val)
}

func TestPromptBotToken_EnvRefWithExisting(t *testing.T) {
	r := newReader("1\nCUSTOM_VAR\n")
	val, err := promptBotToken(r, "existing-token")
	require.NoError(t, err)
	assert.Equal(t, "${CUSTOM_VAR}", val)
}

// ── runWithBaseURL integration tests ──

// setupFakeCodex creates a fake codex binary that prints a version string.
func setupFakeCodex(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\necho 'codex 1.0.0'\n"), 0o755))
	t.Setenv("PATH", binDir)
}

func TestRunWithBaseURL_FullWizard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"id": 42, "username": "mybot"},
		})
	}))
	defer ts.Close()

	// New flow: choice 1 (add Telegram), name (default), token choice 2 (plaintext), the token,
	// done (4)
	simulateStdin(t, "1\n\n2\nfake-token-123\n5\n")

	err := runWithBaseURL(ts.URL + "/bot")
	require.NoError(t, err)

	// Verify config was saved with skeleton values.
	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	ch := MustParseTelegramChannel(cfg.Channels["telegram"])
	assert.Equal(t, "fake-token-123", ch.BotToken)
	assert.NotNil(t, ch.Enabled)
	assert.True(t, *ch.Enabled)
	assert.Equal(t, "pairing", ch.DMPolicy)
	assert.Equal(t, "partial", ch.Streaming)
	assert.Equal(t, "length", ch.ChunkMode)
	assert.Equal(t, 3500, ch.TextChunkLimit)
	// AllowFrom is nil after round-trip due to omitempty + empty slice.
	// Codex defaults.
	assert.Equal(t, "workspace-write", cfg.Codex.Sandbox)
	assert.Equal(t, "read-only", cfg.Codex.GroupSandbox)
	assert.Equal(t, "120m", cfg.Codex.Timeout)
	// Gateway defaults.
	assert.Equal(t, ":8080", cfg.Gateway.Address)
}

func TestRunWithBaseURL_EnvVarToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)
	t.Setenv("MY_BOT_TOKEN", "resolved-token-456")

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"id": 99, "username": "envbot"},
		})
	}))
	defer ts.Close()

	// New flow: choice 1 (add Telegram), name (default), token choice 1 (env var),
	// var name, done (4)
	simulateStdin(t, "1\n\n1\nMY_BOT_TOKEN\n5\n")

	err := runWithBaseURL(ts.URL + "/bot")
	require.NoError(t, err)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	ch := MustParseTelegramChannel(cfg.Channels["telegram"])
	assert.Equal(t, "${MY_BOT_TOKEN}", ch.BotToken)
}

func TestRunWithBaseURL_CodexNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir()) // empty PATH

	err := runWithBaseURL("http://unused/bot")
	assert.ErrorContains(t, err, "codex CLI not found")
}

func TestRunWithBaseURL_TokenRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":          false,
			"description": "Unauthorized",
		})
	}))
	defer ts.Close()

	// New flow: choice 1 (add Telegram), name (default), token choice 2 (plaintext), a token
	simulateStdin(t, "1\n\n2\nbad-token\n")

	err := runWithBaseURL(ts.URL + "/bot")
	assert.ErrorContains(t, err, "telegram rejected the token")
}

func TestRunWithBaseURL_UnresolvableToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)
	// Unset the env var so resolution fails
	os.Unsetenv("NONEXISTENT_VAR_XYZ")

	// New flow: choice 1 (add Telegram), name (default), token choice 1 (env var), var name
	simulateStdin(t, "1\n\n1\nNONEXISTENT_VAR_XYZ\n")

	err := runWithBaseURL("http://unused/bot")
	assert.ErrorContains(t, err, "resolve bot token")
}

// ── WeCom onboarding tests ──

func TestRunWithBaseURL_WeComOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)

	// New flow: choice 2 (add WeCom), name (default), connection mode 1 (webhook),
	// token choice 2 (plaintext), token value,
	// aes key choice 2 (plaintext), aes key value, webhook path choice 2 (plaintext), path value,
	// done (4)
	simulateStdin(t, "2\n\n1\n2\nmy-wecom-token\n2\nabcdefghijklmnopqrstuvwxyz0123456789ABCDEFG\n2\n/wecom/webhook\n5\n")

	err := runWithBaseURL("http://unused/bot")
	require.NoError(t, err)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	require.Len(t, cfg.Channels, 1)
	wc := MustParseWeComChannel(cfg.Channels["wecom"])
	assert.Equal(t, "my-wecom-token", wc.Token)
	assert.Equal(t, "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG", wc.EncodingAESKey)
	assert.NotNil(t, wc.Enabled)
	assert.True(t, *wc.Enabled)
	assert.Equal(t, "/wecom/webhook", wc.WebhookPath)
	// Verify skeleton fields.
	assert.Equal(t, "pairing", wc.DMPolicy)
	assert.Equal(t, "allowlist", wc.GroupPolicy)
	assert.Equal(t, 4096, wc.TextChunkLimit)
	// Codex skeleton.
	assert.Equal(t, "read-only", cfg.Codex.GroupSandbox)
}

func TestRunWithBaseURL_WeComCustomPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)

	// New flow: choice 2 (add WeCom), name (default), connection mode 1 (webhook),
	// token choice 2 (plaintext), token value,
	// aes key choice 2 (plaintext), aes key value, webhook path choice 2 (plaintext), custom path,
	// done (4)
	simulateStdin(t, "2\n\n1\n2\nmy-token\n2\nmy-aes-key-43chars-padded-000000000000000\n2\n/custom/path\n5\n")

	err := runWithBaseURL("http://unused/bot")
	require.NoError(t, err)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	wc := MustParseWeComChannel(cfg.Channels["wecom"])
	assert.Equal(t, "/custom/path", wc.WebhookPath)
}

func TestRunWithBaseURL_WeComEnvVar(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)
	t.Setenv("WECOM_TOKEN", "resolved-token")
	t.Setenv("WECOM_ENCODING_AES_KEY", "resolved-key")

	// New flow: choice 2 (add WeCom), name (default), connection mode 1 (webhook),
	// token choice 1 (env), default var,
	// aes key choice 1 (env), default var, webhook path choice 1 (env), default var,
	// done (4)
	simulateStdin(t, "2\n\n1\n1\n\n1\n\n1\n\n5\n")

	err := runWithBaseURL("http://unused/bot")
	require.NoError(t, err)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	wc := MustParseWeComChannel(cfg.Channels["wecom"])
	assert.Equal(t, "${WECOM_TOKEN}", wc.Token)
	assert.Equal(t, "${WECOM_ENCODING_AES_KEY}", wc.EncodingAESKey)
	assert.Equal(t, "${WECOM_WEBHOOK_PATH}", wc.WebhookPath)
}

func TestRunWithBaseURL_BothChannels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"id": 42, "username": "mybot"},
		})
	}))
	defer ts.Close()

	// New flow:
	// 1. Add Telegram instance (choice 1), name (default), token choice 2 (plaintext),
	//    token value
	// 2. Add WeCom instance (choice 2), name (default), connection mode 1 (webhook),
	//    token choice 2 (plaintext), token value, aes key choice 2 (plaintext), aes key,
	//    webhook path choice 2 (plaintext), path
	// 3. Done (choice 5)
	simulateStdin(t, "1\n\n2\ntg-token\n2\n\n1\n2\nwc-token\n2\nwc-aes-key-value-43chars-padded-00000000000\n2\n/wecom/hook\n5\n")

	err := runWithBaseURL(ts.URL + "/bot")
	require.NoError(t, err)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	tg := MustParseTelegramChannel(cfg.Channels["telegram"])
	assert.Equal(t, "tg-token", tg.BotToken)
	wc := MustParseWeComChannel(cfg.Channels["wecom"])
	assert.Equal(t, "wc-token", wc.Token)
	assert.Equal(t, "wc-aes-key-value-43chars-padded-00000000000", wc.EncodingAESKey)
}

func TestRunWithBaseURL_WeComEmptyWebhookPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)

	// New flow: choice 2 (add WeCom), name (default), connection mode 1 (webhook),
	// token choice 2 (plaintext), token value,
	// aes key choice 2 (plaintext), aes key value,
	// webhook path choice 2 (plaintext), empty value (loops), then valid path
	// done (4)
	simulateStdin(t, "2\n\n1\n2\nmy-token\n2\nmy-aes-key-43chars-padded-000000000000000\n2\n\n2\n/valid/path\n5\n")

	err := runWithBaseURL("http://unused/bot")
	require.NoError(t, err)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	wc := MustParseWeComChannel(cfg.Channels["wecom"])
	assert.Equal(t, "/valid/path", wc.WebhookPath)
}

func TestRunWithBaseURL_InvalidChannelChoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"id": 42, "username": "mybot"},
		})
	}))
	defer ts.Close()

	// Invalid choice 9 loops back; then valid choice 1 (add Telegram),
	// name (default), token choice 2 (plaintext), token, done (5)
	simulateStdin(t, "9\n1\n\n2\ntest-token\n5\n")

	err := runWithBaseURL(ts.URL + "/bot")
	require.NoError(t, err)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	ch := MustParseTelegramChannel(cfg.Channels["telegram"])
	assert.Equal(t, "test-token", ch.BotToken)
}

// ── promptSecret tests ──

func TestPromptSecret_EnvRef(t *testing.T) {
	r := newReader("1\n\n")
	val, err := promptSecret(r, "Token", "MY_VAR", "")
	require.NoError(t, err)
	assert.Equal(t, "${MY_VAR}", val)
}

func TestPromptSecret_Plaintext(t *testing.T) {
	r := newReader("2\nmy-secret\n")
	val, err := promptSecret(r, "Token", "MY_VAR", "")
	require.NoError(t, err)
	assert.Equal(t, "my-secret", val)
}

func TestPromptSecret_PlaintextEmpty(t *testing.T) {
	// After empty input, loop continues; then provide valid plaintext.
	r := newReader("2\n\n2\nmy-secret\n")
	val, err := promptSecret(r, "Token", "MY_VAR", "")
	require.NoError(t, err)
	assert.Equal(t, "my-secret", val)
}

func TestPromptSecret_FileRef(t *testing.T) {
	r := newReader("3\n/run/secrets/key\n")
	val, err := promptSecret(r, "Token", "MY_VAR", "")
	require.NoError(t, err)
	assert.Equal(t, "file:///run/secrets/key", val)
}

func TestPromptSecret_KeepCurrent(t *testing.T) {
	r := newReader("4\n")
	val, err := promptSecret(r, "Token", "MY_VAR", "${EXISTING}")
	require.NoError(t, err)
	assert.Equal(t, "${EXISTING}", val)
}

func TestPromptSecret_KeepCurrentEmpty(t *testing.T) {
	// After no current value, loop continues; then provide valid choice.
	r := newReader("4\n1\n\n")
	val, err := promptSecret(r, "Token", "MY_VAR", "")
	require.NoError(t, err)
	assert.Equal(t, "${MY_VAR}", val)
}

// ── ParseChannelConfig tests ──

func TestParseChannelConfig_Telegram(t *testing.T) {
	raw := json.RawMessage(`{"type":"telegram","bot_token":"tok","dm_policy":"open"}`)
	cfg, err := ParseChannelConfig(raw)
	require.NoError(t, err)
	tg, ok := cfg.(TelegramChannelConfig)
	require.True(t, ok)
	assert.Equal(t, "telegram", tg.Type)
	assert.Equal(t, "tok", tg.BotToken)
	assert.Equal(t, "open", tg.DMPolicy)
}

func TestParseChannelConfig_WeCom(t *testing.T) {
	raw := json.RawMessage(`{"type":"wecom","token":"wc-tok","dm_policy":"pairing"}`)
	cfg, err := ParseChannelConfig(raw)
	require.NoError(t, err)
	wc, ok := cfg.(WeComChannelConfig)
	require.True(t, ok)
	assert.Equal(t, "wecom", wc.Type)
	assert.Equal(t, "wc-tok", wc.Token)
	assert.Equal(t, "pairing", wc.DMPolicy)
}

func TestParseChannelConfig_Weixin(t *testing.T) {
	raw := json.RawMessage(`{"type":"weixin","base_url":"http://example.com","token":"wx-tok"}`)
	cfg, err := ParseChannelConfig(raw)
	require.NoError(t, err)
	wx, ok := cfg.(WeixinChannelConfig)
	require.True(t, ok)
	assert.Equal(t, "weixin", wx.Type)
	assert.Equal(t, "http://example.com", wx.BaseURL)
	assert.Equal(t, "wx-tok", wx.Token)
}

func TestParseChannelConfig_QQBot(t *testing.T) {
	raw := json.RawMessage(`{"type":"qqbot","app_id":"12345","client_secret":"sec"}`)
	cfg, err := ParseChannelConfig(raw)
	require.NoError(t, err)
	qq, ok := cfg.(QQBotChannelConfig)
	require.True(t, ok)
	assert.Equal(t, "qqbot", qq.Type)
	assert.Equal(t, "12345", qq.AppID)
	assert.Equal(t, "sec", qq.ClientSecret)
}

func TestParseChannelConfig_UnknownType(t *testing.T) {
	raw := json.RawMessage(`{"type":"slack"}`)
	_, err := ParseChannelConfig(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown channel type: slack")
}

func TestParseChannelConfig_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`{invalid`)
	_, err := ParseChannelConfig(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse channel type")
}

func TestParseChannelConfig_InvalidTelegramJSON(t *testing.T) {
	// Type parses fine but telegram fields fail (allow_from is not int array)
	raw := json.RawMessage(`{"type":"telegram","allow_from":"not-array"}`)
	_, err := ParseChannelConfig(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse telegram config")
}

func TestParseChannelConfig_InvalidWeComJSON(t *testing.T) {
	raw := json.RawMessage(`{"type":"wecom","allow_from":123}`)
	_, err := ParseChannelConfig(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse wecom config")
}

func TestParseChannelConfig_InvalidWeixinJSON(t *testing.T) {
	raw := json.RawMessage(`{"type":"weixin","allow_from":123}`)
	_, err := ParseChannelConfig(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse weixin config")
}

func TestParseChannelConfig_InvalidQQBotJSON(t *testing.T) {
	raw := json.RawMessage(`{"type":"qqbot","allow_from":123}`)
	_, err := ParseChannelConfig(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse qqbot config")
}

// ── ParseChannelConfigs tests ──

func TestParseChannelConfigs_Multiple(t *testing.T) {
	rawMap := map[string]json.RawMessage{
		"tg": json.RawMessage(`{"type":"telegram","bot_token":"t1"}`),
		"wc": json.RawMessage(`{"type":"wecom","token":"w1"}`),
		"wx": json.RawMessage(`{"type":"weixin","token":"x1"}`),
		"qq": json.RawMessage(`{"type":"qqbot","app_id":"a1"}`),
	}
	result, err := ParseChannelConfigs(rawMap)
	require.NoError(t, err)
	assert.Len(t, result, 4)

	_, ok := result["tg"].(TelegramChannelConfig)
	assert.True(t, ok)
	_, ok = result["wc"].(WeComChannelConfig)
	assert.True(t, ok)
	_, ok = result["wx"].(WeixinChannelConfig)
	assert.True(t, ok)
	_, ok = result["qq"].(QQBotChannelConfig)
	assert.True(t, ok)
}

func TestParseChannelConfigs_Empty(t *testing.T) {
	result, err := ParseChannelConfigs(map[string]json.RawMessage{})
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestParseChannelConfigs_Error(t *testing.T) {
	rawMap := map[string]json.RawMessage{
		"bad": json.RawMessage(`{"type":"unknown_type"}`),
	}
	_, err := ParseChannelConfigs(rawMap)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `channel "bad"`)
}

// ── ChannelType tests ──

func TestChannelType_Telegram(t *testing.T) {
	raw := json.RawMessage(`{"type":"telegram","bot_token":"x"}`)
	typ, err := ChannelType(raw)
	require.NoError(t, err)
	assert.Equal(t, "telegram", typ)
}

func TestChannelType_WeCom(t *testing.T) {
	raw := json.RawMessage(`{"type":"wecom"}`)
	typ, err := ChannelType(raw)
	require.NoError(t, err)
	assert.Equal(t, "wecom", typ)
}

func TestChannelType_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`not-json`)
	_, err := ChannelType(raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse channel type")
}

func TestChannelType_EmptyType(t *testing.T) {
	raw := json.RawMessage(`{"something":"else"}`)
	typ, err := ChannelType(raw)
	require.NoError(t, err)
	assert.Equal(t, "", typ)
}

// ── MarshalWeixinChannel tests ──

func TestMarshalWeixinChannel(t *testing.T) {
	enabled := true
	cfg := WeixinChannelConfig{
		Type:           "weixin",
		Enabled:        &enabled,
		BaseURL:        "http://example.com",
		Token:          "wx-token",
		DMPolicy:       "open",
		AllowFrom:      []string{"user1"},
		TextChunkLimit: 4000,
	}
	raw := MarshalWeixinChannel(cfg)
	assert.NotEmpty(t, raw)

	// Round-trip: unmarshal back
	var result WeixinChannelConfig
	err := json.Unmarshal(raw, &result)
	require.NoError(t, err)
	assert.Equal(t, cfg.Type, result.Type)
	assert.Equal(t, cfg.BaseURL, result.BaseURL)
	assert.Equal(t, cfg.Token, result.Token)
	assert.Equal(t, cfg.DMPolicy, result.DMPolicy)
	assert.Equal(t, cfg.AllowFrom, result.AllowFrom)
	assert.Equal(t, cfg.TextChunkLimit, result.TextChunkLimit)
	require.NotNil(t, result.Enabled)
	assert.True(t, *result.Enabled)
}

func TestMarshalWeixinChannel_Minimal(t *testing.T) {
	cfg := WeixinChannelConfig{Type: "weixin"}
	raw := MarshalWeixinChannel(cfg)
	assert.Contains(t, string(raw), `"type":"weixin"`)
}

// ── SoulPath tests ──

func TestSoulPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	path, err := SoulPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".clawdex", "SOUL.md"), path)
}

// ── InstanceSoulPath tests ──

func TestInstanceSoulPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	path, err := InstanceSoulPath("work")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, ".clawdex", "SOUL-work.md"), path)
}

func TestInstanceSoulPath_Empty(t *testing.T) {
	path, err := InstanceSoulPath("")
	require.NoError(t, err)
	assert.Equal(t, "", path)
}

// ── applyWeixinDefaults tests ──

func TestApplyWeixinDefaults_AllEmpty(t *testing.T) {
	ch := &WeixinChannelConfig{}
	applyWeixinDefaults(ch)

	assert.Equal(t, "weixin", ch.Type)
	require.NotNil(t, ch.Enabled)
	assert.True(t, *ch.Enabled)
	assert.Equal(t, "open", ch.DMPolicy)
	assert.Equal(t, []string{}, ch.AllowFrom)
	assert.Equal(t, 4000, ch.TextChunkLimit)
}

func TestApplyWeixinDefaults_PreservesExisting(t *testing.T) {
	enabled := false
	ch := &WeixinChannelConfig{
		Type:           "weixin",
		Enabled:        &enabled,
		DMPolicy:       "pairing",
		AllowFrom:      []string{"user1"},
		TextChunkLimit: 5000,
	}
	applyWeixinDefaults(ch)

	assert.Equal(t, "weixin", ch.Type)
	require.NotNil(t, ch.Enabled)
	assert.False(t, *ch.Enabled) // preserved
	assert.Equal(t, "pairing", ch.DMPolicy)
	assert.Equal(t, []string{"user1"}, ch.AllowFrom)
	assert.Equal(t, 5000, ch.TextChunkLimit)
}

func TestApplyWeixinDefaults_Nil(t *testing.T) {
	// Should not panic
	applyWeixinDefaults(nil)
}

// ── applyWeComDefaults tests ──

func TestApplyWeComDefaults_AllEmpty(t *testing.T) {
	ch := &WeComChannelConfig{}
	applyWeComDefaults(ch)

	assert.Equal(t, "wecom", ch.Type)
	assert.Equal(t, "pairing", ch.DMPolicy)
	assert.Equal(t, []string{}, ch.AllowFrom)
	assert.Equal(t, "allowlist", ch.GroupPolicy)
	assert.Equal(t, []string{}, ch.GroupAllowFrom)
	assert.Equal(t, 4096, ch.TextChunkLimit)
}

func TestApplyWeComDefaults_PreservesExisting(t *testing.T) {
	ch := &WeComChannelConfig{
		Type:           "wecom",
		DMPolicy:       "open",
		AllowFrom:      []string{"admin"},
		GroupPolicy:    "open",
		GroupAllowFrom: []string{"group1"},
		TextChunkLimit: 2048,
	}
	applyWeComDefaults(ch)

	assert.Equal(t, "wecom", ch.Type)
	assert.Equal(t, "open", ch.DMPolicy)
	assert.Equal(t, []string{"admin"}, ch.AllowFrom)
	assert.Equal(t, "open", ch.GroupPolicy)
	assert.Equal(t, []string{"group1"}, ch.GroupAllowFrom)
	assert.Equal(t, 2048, ch.TextChunkLimit)
}

func TestApplyWeComDefaults_Nil(t *testing.T) {
	// Should not panic
	applyWeComDefaults(nil)
}

// ── WithInstallDaemon tests ──

func TestWithInstallDaemon_True(t *testing.T) {
	var opts runOptions
	WithInstallDaemon(true)(&opts)
	assert.True(t, opts.installDaemon)
}

func TestWithInstallDaemon_False(t *testing.T) {
	var opts runOptions
	WithInstallDaemon(false)(&opts)
	assert.False(t, opts.installDaemon)
}

// ── hasMeaningfulConfig tests ──

func TestHasMeaningfulConfig_Nil(t *testing.T) {
	assert.False(t, hasMeaningfulConfig(nil))
}

func TestHasMeaningfulConfig_Empty(t *testing.T) {
	assert.False(t, hasMeaningfulConfig(&FileConfig{}))
}

func TestHasMeaningfulConfig_WithWorkDir(t *testing.T) {
	assert.True(t, hasMeaningfulConfig(&FileConfig{
		Codex: CodexFileConfig{WorkDir: "/tmp"},
	}))
}

func TestHasMeaningfulConfig_WithTimeout(t *testing.T) {
	assert.True(t, hasMeaningfulConfig(&FileConfig{
		Codex: CodexFileConfig{Timeout: "10m"},
	}))
}

func TestHasMeaningfulConfig_WithMaxOutputRunes(t *testing.T) {
	assert.True(t, hasMeaningfulConfig(&FileConfig{
		Codex: CodexFileConfig{MaxOutputRunes: 1000},
	}))
}

func TestHasMeaningfulConfig_WithSandbox(t *testing.T) {
	assert.True(t, hasMeaningfulConfig(&FileConfig{
		Codex: CodexFileConfig{Sandbox: "read-only"},
	}))
}

func TestHasMeaningfulConfig_WithGroupSandbox(t *testing.T) {
	assert.True(t, hasMeaningfulConfig(&FileConfig{
		Codex: CodexFileConfig{GroupSandbox: "workspace-write"},
	}))
}

func TestHasMeaningfulConfig_WithGatewayAddress(t *testing.T) {
	assert.True(t, hasMeaningfulConfig(&FileConfig{
		Gateway: GatewayFileConfig{Address: ":9090"},
	}))
}

func TestHasMeaningfulConfig_WithLoggingLevel(t *testing.T) {
	assert.True(t, hasMeaningfulConfig(&FileConfig{
		Logging: LoggingFileConfig{Level: "debug"},
	}))
}

func TestHasMeaningfulConfig_WithLoggingFormat(t *testing.T) {
	assert.True(t, hasMeaningfulConfig(&FileConfig{
		Logging: LoggingFileConfig{Format: "json"},
	}))
}

func TestHasMeaningfulConfig_WithLoggingCodexFile(t *testing.T) {
	assert.True(t, hasMeaningfulConfig(&FileConfig{
		Logging: LoggingFileConfig{CodexFile: "/tmp/codex.log"},
	}))
}

func TestHasMeaningfulConfig_WithChannels(t *testing.T) {
	assert.True(t, hasMeaningfulConfig(&FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": json.RawMessage(`{"type":"telegram"}`),
		},
	}))
}

// ── applyChannelDefaults tests ──

func TestApplyChannelDefaults_NilConfig(t *testing.T) {
	err := applyChannelDefaults(nil)
	assert.NoError(t, err)
}

func TestApplyChannelDefaults_EmptyChannels(t *testing.T) {
	cfg := &FileConfig{}
	err := applyChannelDefaults(cfg)
	assert.NoError(t, err)
}

func TestApplyChannelDefaults_Telegram(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": json.RawMessage(`{"type":"telegram","bot_token":"tok"}`),
		},
	}
	err := applyChannelDefaults(cfg)
	require.NoError(t, err)

	ch := MustParseTelegramChannel(cfg.Channels["tg"])
	assert.Equal(t, "telegram", ch.Type)
	assert.Equal(t, "pairing", ch.DMPolicy)
	assert.Equal(t, "length", ch.ChunkMode)
	assert.Equal(t, 3500, ch.TextChunkLimit)
	assert.Equal(t, "partial", ch.Streaming)
	assert.Equal(t, "allowlist", ch.GroupPolicy)
	require.NotNil(t, ch.Enabled)
	assert.True(t, *ch.Enabled)
	require.NotNil(t, ch.RequireMention)
	assert.True(t, *ch.RequireMention)
}

func TestApplyChannelDefaults_WeCom(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"wc": json.RawMessage(`{"type":"wecom","token":"wc-tok"}`),
		},
	}
	err := applyChannelDefaults(cfg)
	require.NoError(t, err)

	var ch WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc"], &ch))
	assert.Equal(t, "wecom", ch.Type)
	assert.Equal(t, "pairing", ch.DMPolicy)
	assert.Equal(t, "allowlist", ch.GroupPolicy)
	assert.Equal(t, 4096, ch.TextChunkLimit)
}

func TestApplyChannelDefaults_Weixin(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": json.RawMessage(`{"type":"weixin","token":"wx-tok"}`),
		},
	}
	err := applyChannelDefaults(cfg)
	require.NoError(t, err)

	var ch WeixinChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wx"], &ch))
	assert.Equal(t, "weixin", ch.Type)
	assert.Equal(t, "open", ch.DMPolicy)
	assert.Equal(t, 4000, ch.TextChunkLimit)
	require.NotNil(t, ch.Enabled)
	assert.True(t, *ch.Enabled)
}

func TestApplyChannelDefaults_InvalidChannelJSON(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"bad": json.RawMessage(`{invalid`),
		},
	}
	err := applyChannelDefaults(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse channel type")
}

func TestApplyChannelDefaults_InvalidTelegramJSON(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": json.RawMessage(`{"type":"telegram","allow_from":"not-an-array"}`),
		},
	}
	err := applyChannelDefaults(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse telegram config")
}

func TestApplyChannelDefaults_InvalidWeComJSON(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"wc": json.RawMessage(`{"type":"wecom","allow_from":123}`),
		},
	}
	err := applyChannelDefaults(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse wecom config")
}

func TestApplyChannelDefaults_InvalidWeixinJSON(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": json.RawMessage(`{"type":"weixin","allow_from":123}`),
		},
	}
	err := applyChannelDefaults(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse weixin config")
}

func TestApplyChannelDefaults_UnknownType(t *testing.T) {
	// Unknown types are not processed — no error, left as-is.
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"slack": json.RawMessage(`{"type":"slack","token":"x"}`),
		},
	}
	err := applyChannelDefaults(cfg)
	assert.NoError(t, err)
}

func TestApplyChannelDefaults_MultipleChannels(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": json.RawMessage(`{"type":"telegram","bot_token":"t"}`),
			"wc": json.RawMessage(`{"type":"wecom","token":"w"}`),
			"wx": json.RawMessage(`{"type":"weixin","token":"x"}`),
		},
	}
	err := applyChannelDefaults(cfg)
	require.NoError(t, err)

	tg := MustParseTelegramChannel(cfg.Channels["tg"])
	assert.Equal(t, "pairing", tg.DMPolicy)

	var wc WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc"], &wc))
	assert.Equal(t, "pairing", wc.DMPolicy)

	var wx WeixinChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wx"], &wx))
	assert.Equal(t, "open", wx.DMPolicy)
}

// ── refuseEmptyConfigOverwrite additional tests ──

func TestRefuseEmptyConfigOverwrite_EmptyOnEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	// Write an empty config file.
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o644))

	// Overwriting empty with empty should be allowed.
	err := refuseEmptyConfigOverwrite(&FileConfig{}, path)
	assert.NoError(t, err)
}

func TestRefuseEmptyConfigOverwrite_NonEmptyOnNonExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	// Overwriting non-existent file should always be allowed.
	err := refuseEmptyConfigOverwrite(&FileConfig{}, path)
	assert.NoError(t, err)
}

func TestRefuseEmptyConfigOverwrite_NonEmptyConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	// Write an existing non-empty config.
	existing := `{"codex":{"workdir":"/tmp"}}`
	require.NoError(t, os.WriteFile(path, []byte(existing), 0o644))

	// Overwriting with a non-empty config should be allowed.
	cfg := &FileConfig{Codex: CodexFileConfig{WorkDir: "/new"}}
	err := refuseEmptyConfigOverwrite(cfg, path)
	assert.NoError(t, err)
}

func TestRefuseEmptyConfigOverwrite_EmptyOnNonEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	// Write a non-empty config.
	existing := `{"codex":{"workdir":"/tmp"}}`
	require.NoError(t, os.WriteFile(path, []byte(existing), 0o644))

	// Overwriting with empty should be refused.
	err := refuseEmptyConfigOverwrite(&FileConfig{}, path)
	assert.ErrorIs(t, err, errRefuseEmptyConfigOverwrite)
}

// ── SaveFileConfig / LoadFileConfig HOME override tests ──

func TestSaveFileConfig_CreatesDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	cfg := &FileConfig{
		Codex: CodexFileConfig{WorkDir: "/test"},
	}
	require.NoError(t, SaveFileConfig(cfg))

	loaded, err := LoadFileConfig()
	require.NoError(t, err)
	assert.Equal(t, "/test", loaded.Codex.WorkDir)
}

func TestSaveFileConfigTo_CreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "dir", "clawdex.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))

	cfg := &FileConfig{Codex: CodexFileConfig{Timeout: "5m"}}
	require.NoError(t, SaveFileConfigTo(cfg, path))

	loaded, err := LoadFileConfigFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "5m", loaded.Codex.Timeout)
}

// ── applyQQBotDefaults via round-trip ──

func TestApplyChannelDefaults_QQBot(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"qq": json.RawMessage(`{"type":"qqbot","app_id":"123","client_secret":"sec"}`),
		},
	}
	// QQBot type is not explicitly handled by applyChannelDefaults (no case),
	// so it should just pass through without error.
	err := applyChannelDefaults(cfg)
	assert.NoError(t, err)
}

// ── QQ Bot onboard wizard test ──

func TestRunWithBaseURL_QQBotOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)

	// New flow: choice 4 (add QQ Bot), name (default), app_id, client_secret, done (5)
	simulateStdin(t, "4\n\nmy-app-id\nmy-client-secret\n5\n")

	err := runWithBaseURL("http://unused/bot")
	require.NoError(t, err)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	require.Len(t, cfg.Channels, 1)

	parsed, err := ParseChannelConfig(cfg.Channels["qqbot"])
	require.NoError(t, err)
	qq, ok := parsed.(QQBotChannelConfig)
	require.True(t, ok)
	assert.Equal(t, "my-app-id", qq.AppID)
	assert.Equal(t, "my-client-secret", qq.ClientSecret)
	assert.NotNil(t, qq.Enabled)
	assert.True(t, *qq.Enabled)
	assert.Equal(t, "open", qq.DMPolicy)
	assert.Equal(t, "open", qq.GroupPolicy)
}

// ── MarshalQQBotChannel tests ──

func TestMarshalQQBotChannel(t *testing.T) {
	enabled := true
	cfg := QQBotChannelConfig{
		Type:         "qqbot",
		Enabled:      &enabled,
		AppID:        "12345",
		ClientSecret: "sec",
		DMPolicy:     "open",
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)

	var result QQBotChannelConfig
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Equal(t, "12345", result.AppID)
	assert.Equal(t, "open", result.DMPolicy)
}

// ── Additional coverage tests (appended) ──

func TestApplyChannelDefaults_TelegramPreservesExistingValues(t *testing.T) {
	// When fields already have values, applyChannelDefaults should preserve them.
	enabled := false
	requireMention := false
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": MarshalTelegramChannel(TelegramChannelConfig{
				Type:           "telegram",
				BotToken:       "tok",
				Enabled:        &enabled,
				DMPolicy:       "open",
				ChunkMode:      "newline",
				TextChunkLimit: 5000,
				Streaming:      "off",
				AllowFrom:      []int64{42},
				GroupPolicy:    "open",
				GroupAllowFrom: []int64{100},
				RequireMention: &requireMention,
			}),
		},
	}
	err := applyChannelDefaults(cfg)
	require.NoError(t, err)

	ch := MustParseTelegramChannel(cfg.Channels["tg"])
	assert.False(t, *ch.Enabled)
	assert.Equal(t, "open", ch.DMPolicy)
	assert.Equal(t, "newline", ch.ChunkMode)
	assert.Equal(t, 5000, ch.TextChunkLimit)
	assert.Equal(t, "off", ch.Streaming)
	assert.Equal(t, []int64{42}, ch.AllowFrom)
	assert.Equal(t, "open", ch.GroupPolicy)
	assert.Equal(t, []int64{100}, ch.GroupAllowFrom)
	assert.False(t, *ch.RequireMention)
}

func TestApplyTelegramDefaults_Nil(t *testing.T) {
	// Should not panic
	applyTelegramDefaults(nil)
}

func TestSaveFileConfigTo_NonExistentDir(t *testing.T) {
	// Write to a path whose parent dir doesn't exist
	path := filepath.Join(t.TempDir(), "deep", "nested", "clawdex.json")
	cfg := &FileConfig{Codex: CodexFileConfig{WorkDir: "/tmp"}}
	err := SaveFileConfigTo(cfg, path)
	// Should fail because parent dir doesn't exist for the temp file
	assert.Error(t, err)
}

func TestSaveFileConfigTo_MetaFieldsUpdated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	cfg := &FileConfig{
		Codex: CodexFileConfig{WorkDir: "/tmp"},
	}
	require.NoError(t, SaveFileConfigTo(cfg, path))

	loaded, err := LoadFileConfigFrom(path)
	require.NoError(t, err)
	assert.NotEmpty(t, loaded.Meta.LastTouchedVersion)
	assert.NotEmpty(t, loaded.Meta.LastTouchedAt)
}

func TestLoadFileConfigFrom_ValidComplex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")

	content := `{
  "meta": {"lastTouchedVersion": "1.0.0"},
  "codex": {"workdir": "/home/user", "timeout": "15m", "max_output_runes": 5000},
  "gateway": {"address": ":9090"},
  "logging": {"level": "debug", "format": "json", "codex_file": "/tmp/codex.log"},
  "channels": {
    "tg": {"type": "telegram", "bot_token": "tok1"},
    "wc": {"type": "wecom", "token": "tok2"},
    "wx": {"type": "weixin", "token": "tok3"},
    "qq": {"type": "qqbot", "app_id": "id1"}
  }
}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	cfg, err := LoadFileConfigFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", cfg.Meta.LastTouchedVersion)
	assert.Equal(t, "/home/user", cfg.Codex.WorkDir)
	assert.Equal(t, "15m", cfg.Codex.Timeout)
	assert.Equal(t, 5000, cfg.Codex.MaxOutputRunes)
	assert.Equal(t, ":9090", cfg.Gateway.Address)
	assert.Equal(t, "debug", cfg.Logging.Level)
	assert.Equal(t, "json", cfg.Logging.Format)
	assert.Equal(t, "/tmp/codex.log", cfg.Logging.CodexFile)
	assert.Len(t, cfg.Channels, 4)
}

func TestRunWithBaseURL_WeComWebSocket(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)

	// New flow: choice 2 (add WeCom), name (default), connection mode 2 (websocket),
	// botid choice 2 (plaintext), botid value,
	// secret choice 2 (plaintext), secret value,
	// done (5)
	simulateStdin(t, "2\n\n2\n2\nmy-bot-id\n2\nmy-secret-value\n5\n")

	err := runWithBaseURL("http://unused/bot")
	require.NoError(t, err)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	wc := MustParseWeComChannel(cfg.Channels["wecom"])
	assert.Equal(t, "websocket", wc.ConnectionMode)
	assert.Equal(t, "my-bot-id", wc.BotID)
	assert.Equal(t, "my-secret-value", wc.Secret)
	assert.Equal(t, "pairing", wc.DMPolicy)
}

func TestRunWithBaseURL_MultipleTelegramInstances(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setupFakeCodex(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"id": 42, "username": "mybot"},
		})
	}))
	defer ts.Close()

	// Add two Telegram instances then done
	simulateStdin(t, "1\n\n2\ntoken-one\n1\ntelegram-2\n2\ntoken-two\n5\n")

	err := runWithBaseURL(ts.URL + "/bot")
	require.NoError(t, err)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	assert.Len(t, cfg.Channels, 2)
}

func TestRefuseEmptyConfigOverwrite_StatError(t *testing.T) {
	// When the path is a directory (not a file), stat won't fail with NotExist,
	// it succeeds — and then LoadFileConfigFrom will error.
	dir := t.TempDir()
	subDir := filepath.Join(dir, "sub")
	require.NoError(t, os.MkdirAll(subDir, 0o755))

	// The path *exists* as a directory, so we get a different flow.
	err := refuseEmptyConfigOverwrite(&FileConfig{}, subDir)
	// LoadFileConfigFrom on a directory will return a read error.
	assert.Error(t, err)
}

// ── Additional coverage tests for onboard package ──

func TestRefuseEmptyConfigOverwrite_ExistingMeaningfulConfig_V2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clawdex.json")
	existing := &FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": MarshalTelegramChannel(TelegramChannelConfig{Type: "telegram", BotToken: "tok"}),
		},
	}
	data, _ := json.Marshal(existing)
	require.NoError(t, os.WriteFile(path, data, 0o644))

	err := refuseEmptyConfigOverwrite(&FileConfig{}, path)
	assert.ErrorIs(t, err, errRefuseEmptyConfigOverwrite)
}

func TestSaveFileConfigTo_CreatesFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	path := filepath.Join(dir, "config.json")

	// Must create the parent directory first.
	require.NoError(t, os.MkdirAll(dir, 0o755))

	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": MarshalTelegramChannel(TelegramChannelConfig{
				Type:     "telegram",
				BotToken: "tok",
			}),
		},
	}
	err := SaveFileConfigTo(cfg, path)
	require.NoError(t, err)

	// Verify file exists and is valid JSON.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.True(t, json.Valid(data))
}

func TestMustParseTelegramChannel_Valid_V2(t *testing.T) {
	raw := MarshalTelegramChannel(TelegramChannelConfig{
		Type:     "telegram",
		BotToken: "123:tok",
	})
	ch := MustParseTelegramChannel(raw)
	assert.Equal(t, "123:tok", ch.BotToken)
}

func TestMustParseTelegramChannel_Invalid_V2(t *testing.T) {
	assert.Panics(t, func() {
		MustParseTelegramChannel(json.RawMessage(`{invalid`))
	})
}

func TestMustParseWeComChannel_Valid_V2(t *testing.T) {
	raw := MarshalWeComChannel(WeComChannelConfig{
		Type:  "wecom",
		Token: "tok",
	})
	ch := MustParseWeComChannel(raw)
	assert.Equal(t, "tok", ch.Token)
}

func TestMustParseWeComChannel_Invalid_V2(t *testing.T) {
	assert.Panics(t, func() {
		MustParseWeComChannel(json.RawMessage(`{invalid`))
	})
}

func TestSaveFileConfig_UpdatesMeta_V2(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	clawdexDir := filepath.Join(home, ".clawdex")
	require.NoError(t, os.MkdirAll(clawdexDir, 0o755))

	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": MarshalTelegramChannel(TelegramChannelConfig{
				Type:     "telegram",
				BotToken: "tok",
			}),
		},
	}
	require.NoError(t, SaveFileConfig(cfg))

	loaded, err := LoadFileConfig()
	require.NoError(t, err)
	assert.NotEmpty(t, loaded.Meta.LastTouchedAt)
}

func TestLoadFileConfig_EmptyHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := LoadFileConfig()
	require.NoError(t, err)
	assert.NotNil(t, cfg)
}

func TestInstanceSoulPath_ValidName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path, err := InstanceSoulPath("my-bot")
	require.NoError(t, err)
	assert.Contains(t, path, "SOUL-my-bot.md")
}

func TestApplyChannelDefaults_WeComGroupDefaults(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"wc": MarshalWeComChannel(WeComChannelConfig{
				Type:  "wecom",
				Token: "tok",
			}),
		},
	}
	require.NoError(t, applyChannelDefaults(cfg))

	var ch WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc"], &ch))
	assert.Equal(t, "allowlist", ch.GroupPolicy)
	assert.NotNil(t, ch.GroupAllowFrom)
	assert.NotNil(t, ch.AllowFrom)
}

func TestApplyChannelDefaults_WeixinFullDefaults(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": MarshalWeixinChannel(WeixinChannelConfig{
				Type:  "weixin",
				Token: "tok",
			}),
		},
	}
	require.NoError(t, applyChannelDefaults(cfg))

	var ch WeixinChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wx"], &ch))
	assert.Equal(t, "open", ch.DMPolicy)
	assert.Equal(t, 4000, ch.TextChunkLimit)
	require.NotNil(t, ch.Enabled)
	assert.True(t, *ch.Enabled)
	assert.NotNil(t, ch.AllowFrom)
}

// ── Additional coverage: SaveFileConfigTo paths, applyChannelDefaults invalid paths ──

func TestApplyChannelDefaults_QQBotType(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"qq": json.RawMessage(`{"type":"qqbot","app_id":"a1"}`),
		},
	}
	// qqbot type not handled in applyChannelDefaults switch, should not error
	err := applyChannelDefaults(cfg)
	assert.NoError(t, err)
}

func TestApplyChannelDefaults_BadTelegramJSON(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": json.RawMessage(`{"type":"telegram","enabled":"bad"}`),
		},
	}
	err := applyChannelDefaults(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse telegram config")
}

func TestApplyChannelDefaults_BadWeComJSON(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"wc": json.RawMessage(`{"type":"wecom","text_chunk_limit":"bad"}`),
		},
	}
	err := applyChannelDefaults(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse wecom config")
}

func TestApplyChannelDefaults_BadWeixinJSON(t *testing.T) {
	cfg := &FileConfig{
		Channels: map[string]json.RawMessage{
			"wx": json.RawMessage(`{"type":"weixin","enabled":"bad"}`),
		},
	}
	err := applyChannelDefaults(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse weixin config")
}

func TestSaveFileConfigTo_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := &FileConfig{
		Codex: CodexFileConfig{WorkDir: "/tmp/atomic-test"},
		Channels: map[string]json.RawMessage{
			"tg": MarshalTelegramChannel(TelegramChannelConfig{
				Type:     "telegram",
				BotToken: "test-token",
			}),
		},
	}
	require.NoError(t, SaveFileConfigTo(cfg, path))

	// Verify file exists and is readable
	loaded, err := LoadFileConfigFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "/tmp/atomic-test", loaded.Codex.WorkDir)
	assert.NotEmpty(t, loaded.Meta.LastTouchedAt)
}

func TestSaveFileConfigTo_MultipleChannelTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.json")

	cfg := &FileConfig{
		Codex: CodexFileConfig{WorkDir: "/workspace"},
		Channels: map[string]json.RawMessage{
			"tg": MarshalTelegramChannel(TelegramChannelConfig{Type: "telegram", BotToken: "t"}),
			"wc": MarshalWeComChannel(WeComChannelConfig{Type: "wecom", Token: "w"}),
			"wx": MarshalWeixinChannel(WeixinChannelConfig{Type: "weixin", Token: "x"}),
		},
	}
	require.NoError(t, SaveFileConfigTo(cfg, path))

	loaded, err := LoadFileConfigFrom(path)
	require.NoError(t, err)
	assert.Len(t, loaded.Channels, 3)

	// Verify defaults were applied
	var tg TelegramChannelConfig
	require.NoError(t, json.Unmarshal(loaded.Channels["tg"], &tg))
	assert.Equal(t, "pairing", tg.DMPolicy)
	require.NotNil(t, tg.Enabled)

	var wc WeComChannelConfig
	require.NoError(t, json.Unmarshal(loaded.Channels["wc"], &wc))
	assert.Equal(t, "pairing", wc.DMPolicy)
	assert.Equal(t, 4096, wc.TextChunkLimit)

	var wx WeixinChannelConfig
	require.NoError(t, json.Unmarshal(loaded.Channels["wx"], &wx))
	assert.Equal(t, "open", wx.DMPolicy)
	assert.Equal(t, 4000, wx.TextChunkLimit)
}

func TestParseChannelConfigs_WithError(t *testing.T) {
	channels := map[string]json.RawMessage{
		"bad": json.RawMessage(`{invalid json`),
	}
	_, err := ParseChannelConfigs(channels)
	assert.Error(t, err)
}

func TestLoadFileConfigFrom_Populated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "populated.json")

	content := `{
		"codex": {"workdir": "/ws", "timeout": "15m", "sandbox": "read-only"},
		"gateway": {"address": ":8080"},
		"logging": {"level": "info"},
		"channels": {
			"tg": {"type": "telegram", "bot_token": "123:ABC"}
		}
	}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	cfg, err := LoadFileConfigFrom(path)
	require.NoError(t, err)
	assert.Equal(t, "/ws", cfg.Codex.WorkDir)
	assert.Equal(t, "15m", cfg.Codex.Timeout)
	assert.Equal(t, "read-only", cfg.Codex.Sandbox)
	assert.Equal(t, ":8080", cfg.Gateway.Address)
	assert.Equal(t, "info", cfg.Logging.Level)
	assert.Len(t, cfg.Channels, 1)
}
