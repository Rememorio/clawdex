package app

import (
	"encoding/json"
	"testing"

	"github.com/Rememorio/clawdex/internal/onboard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── parseChannelKey tests ──

func TestParseChannelKey_Valid(t *testing.T) {
	tests := []struct {
		key       string
		wantName  string
		wantField string
	}{
		{
			key:       "channels.telegram.bot_token",
			wantName:  "telegram",
			wantField: "bot_token",
		},
		{
			key:       "channels.my-wecom.token",
			wantName:  "my-wecom",
			wantField: "token",
		},
		{
			key:       "channels.*.dm_policy",
			wantName:  "*",
			wantField: "dm_policy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			name, field, ok := parseChannelKey(tt.key)
			require.True(t, ok)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantField, field)
		})
	}
}

func TestParseChannelKey_Invalid(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "no_prefix", key: "codex.workdir"},
		{name: "empty_after_prefix", key: "channels."},
		{name: "no_dot_after_name", key: "channels.telegram"},
		{name: "empty_name", key: "channels..token"},
		{name: "empty_field", key: "channels.telegram."},
		{name: "totally_empty", key: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, ok := parseChannelKey(tt.key)
			assert.False(t, ok)
		})
	}
}

// ── parseBool tests ──

func TestParseBool_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"1", true},
		{"false", false},
		{"False", false},
		{"FALSE", false},
		{"0", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseBool(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseBool_Invalid(t *testing.T) {
	for _, input := range []string{"yes", "no", "2", ""} {
		t.Run(input, func(t *testing.T) {
			_, err := parseBool(input)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "invalid boolean")
		})
	}
}

// ── validateChoice tests ──

func TestValidateChoice_Match(t *testing.T) {
	err := validateChoice("info", "debug", "info", "warn", "error")
	assert.NoError(t, err)
}

func TestValidateChoice_NoMatch(t *testing.T) {
	err := validateChoice("trace", "debug", "info", "warn", "error")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid value")
	assert.Contains(t, err.Error(), "trace")
}

// ── fmtBoolPtr tests ──

func TestFmtBoolPtr(t *testing.T) {
	trueVal := true
	falseVal := false
	assert.Equal(t, "", fmtBoolPtr(nil))
	assert.Equal(t, "true", fmtBoolPtr(&trueVal))
	assert.Equal(t, "false", fmtBoolPtr(&falseVal))
}

// ── fmtInt tests ──

func TestFmtInt(t *testing.T) {
	assert.Equal(t, "", fmtInt(0))
	assert.Equal(t, "42", fmtInt(42))
	assert.Equal(t, "-1", fmtInt(-1))
}

// ── fmtInt64Slice tests ──

func TestFmtInt64Slice(t *testing.T) {
	assert.Equal(t, "", fmtInt64Slice(nil))
	assert.Equal(t, "", fmtInt64Slice([]int64{}))
	assert.Equal(t, "100", fmtInt64Slice([]int64{100}))
	assert.Equal(t, "1,2,3", fmtInt64Slice([]int64{1, 2, 3}))
}

// ── fmtStringSlice tests ──

func TestFmtStringSlice(t *testing.T) {
	assert.Equal(t, "", fmtStringSlice(nil))
	assert.Equal(t, "", fmtStringSlice([]string{}))
	assert.Equal(t, "alice", fmtStringSlice([]string{"alice"}))
	assert.Equal(t, "alice,bob", fmtStringSlice([]string{"alice", "bob"}))
}

// ── parseCommaSepInt64 tests ──

func TestParseCommaSepInt64_Valid(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []int64
	}{
		{name: "single", input: "42", want: []int64{42}},
		{name: "multiple", input: "1,2,3", want: []int64{1, 2, 3}},
		{name: "spaces", input: " 10 , 20 ", want: []int64{10, 20}},
		{name: "empty_string", input: "", want: nil},
		{name: "whitespace_only", input: "   ", want: nil},
		{
			name:  "trailing_comma",
			input: "100,200,",
			want:  []int64{100, 200},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCommaSepInt64(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseCommaSepInt64_Invalid(t *testing.T) {
	_, err := parseCommaSepInt64("abc")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid int64")
}

// ── parseCommaSepStrings tests ──

func TestParseCommaSepStrings(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "single", input: "alice", want: []string{"alice"}},
		{
			name:  "multiple",
			input: "alice,bob",
			want:  []string{"alice", "bob"},
		},
		{
			name:  "spaces",
			input: " alice , bob ",
			want:  []string{"alice", "bob"},
		},
		{name: "empty_parts", input: ",a,,b,", want: []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCommaSepStrings(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ── fmtJSON tests ──

func TestFmtJSON(t *testing.T) {
	assert.Equal(t, "", fmtJSON(nil))
	assert.Equal(t, "", fmtJSON(map[string]string{}))

	m := map[string]int{"a": 1}
	got := fmtJSON(m)
	assert.Equal(t, `{"a":1}`, got)
}

// ── telegramFields getter/setter tests ──

func TestTelegramFields_TypeField(t *testing.T) {
	entry := telegramFields["type"]
	ch := &onboard.TelegramChannelConfig{Type: "telegram"}
	assert.Equal(t, "telegram", entry.get(ch))

	// Valid set.
	err := entry.set(ch, "telegram")
	assert.NoError(t, err)
	assert.Equal(t, "telegram", ch.Type)

	// Invalid set.
	err = entry.set(ch, "wecom")
	assert.Error(t, err)
}

func TestTelegramFields_BotToken(t *testing.T) {
	entry := telegramFields["bot_token"]
	ch := &onboard.TelegramChannelConfig{}

	assert.Equal(t, "", entry.get(ch))
	assert.True(t, entry.secret)

	err := entry.set(ch, "tok123")
	require.NoError(t, err)
	assert.Equal(t, "tok123", ch.BotToken)
	assert.Equal(t, "tok123", entry.get(ch))
}

func TestTelegramFields_Enabled(t *testing.T) {
	entry := telegramFields["enabled"]
	ch := &onboard.TelegramChannelConfig{}

	// nil => "".
	assert.Equal(t, "", entry.get(ch))

	err := entry.set(ch, "true")
	require.NoError(t, err)
	assert.Equal(t, "true", entry.get(ch))

	err = entry.set(ch, "false")
	require.NoError(t, err)
	assert.Equal(t, "false", entry.get(ch))

	err = entry.set(ch, "nope")
	assert.Error(t, err)
}

func TestTelegramFields_DMPolicy(t *testing.T) {
	entry := telegramFields["dm_policy"]
	ch := &onboard.TelegramChannelConfig{}

	for _, v := range []string{"open", "pairing", "allowlist"} {
		require.NoError(t, entry.set(ch, v))
		assert.Equal(t, v, entry.get(ch))
	}
	assert.Error(t, entry.set(ch, "blocked"))
}

func TestTelegramFields_ChunkMode(t *testing.T) {
	entry := telegramFields["chunk_mode"]
	ch := &onboard.TelegramChannelConfig{}

	require.NoError(t, entry.set(ch, "length"))
	assert.Equal(t, "length", entry.get(ch))

	require.NoError(t, entry.set(ch, "newline"))
	assert.Equal(t, "newline", entry.get(ch))

	assert.Error(t, entry.set(ch, "word"))
}

func TestTelegramFields_TextChunkLimit(t *testing.T) {
	entry := telegramFields["text_chunk_limit"]
	ch := &onboard.TelegramChannelConfig{}

	require.NoError(t, entry.set(ch, "4000"))
	assert.Equal(t, 4000, ch.TextChunkLimit)
	assert.Equal(t, "4000", entry.get(ch))

	assert.Error(t, entry.set(ch, "abc"))
}

func TestTelegramFields_Streaming(t *testing.T) {
	entry := telegramFields["streaming"]
	ch := &onboard.TelegramChannelConfig{}

	for _, v := range []string{"off", "partial", "progress"} {
		require.NoError(t, entry.set(ch, v))
		assert.Equal(t, v, entry.get(ch))
	}
	assert.Error(t, entry.set(ch, "full"))
}

func TestTelegramFields_AllowFrom(t *testing.T) {
	entry := telegramFields["allow_from"]
	ch := &onboard.TelegramChannelConfig{}

	require.NoError(t, entry.set(ch, "100,200"))
	assert.Equal(t, []int64{100, 200}, ch.AllowFrom)
	assert.Equal(t, "100,200", entry.get(ch))

	assert.Error(t, entry.set(ch, "bad"))
}

func TestTelegramFields_GroupPolicy(t *testing.T) {
	entry := telegramFields["group_policy"]
	ch := &onboard.TelegramChannelConfig{}

	for _, v := range []string{"disabled", "allowlist", "open"} {
		require.NoError(t, entry.set(ch, v))
		assert.Equal(t, v, entry.get(ch))
	}
	assert.Error(t, entry.set(ch, "invite"))
}

func TestTelegramFields_GroupAllowFrom(t *testing.T) {
	entry := telegramFields["group_allow_from"]
	ch := &onboard.TelegramChannelConfig{}

	require.NoError(t, entry.set(ch, "10,20"))
	assert.Equal(t, []int64{10, 20}, ch.GroupAllowFrom)
	assert.Equal(t, "10,20", entry.get(ch))
}

func TestTelegramFields_RequireMention(t *testing.T) {
	entry := telegramFields["require_mention"]
	ch := &onboard.TelegramChannelConfig{}

	assert.Equal(t, "", entry.get(ch))

	require.NoError(t, entry.set(ch, "true"))
	assert.Equal(t, "true", entry.get(ch))

	require.NoError(t, entry.set(ch, "false"))
	assert.Equal(t, "false", entry.get(ch))
}

// ── wecomFields getter/setter tests ──

func TestWeComFields_TypeField(t *testing.T) {
	entry := wecomFields["type"]
	ch := &onboard.WeComChannelConfig{Type: "wecom"}
	assert.Equal(t, "wecom", entry.get(ch))

	require.NoError(t, entry.set(ch, "wecom"))
	assert.Error(t, entry.set(ch, "telegram"))
}

func TestWeComFields_Token(t *testing.T) {
	entry := wecomFields["token"]
	ch := &onboard.WeComChannelConfig{}

	assert.True(t, entry.secret)
	require.NoError(t, entry.set(ch, "mytoken"))
	assert.Equal(t, "mytoken", entry.get(ch))
}

func TestWeComFields_EncodingAESKey(t *testing.T) {
	entry := wecomFields["encoding_aes_key"]
	ch := &onboard.WeComChannelConfig{}

	assert.True(t, entry.secret)
	require.NoError(t, entry.set(ch, "aeskey123"))
	assert.Equal(t, "aeskey123", entry.get(ch))
}

func TestWeComFields_WebhookPath(t *testing.T) {
	entry := wecomFields["webhook_path"]
	ch := &onboard.WeComChannelConfig{}

	require.NoError(t, entry.set(ch, "/wecom/callback"))
	assert.Equal(t, "/wecom/callback", entry.get(ch))
}

func TestWeComFields_Enabled(t *testing.T) {
	entry := wecomFields["enabled"]
	ch := &onboard.WeComChannelConfig{}

	assert.Equal(t, "", entry.get(ch))

	require.NoError(t, entry.set(ch, "true"))
	assert.Equal(t, "true", entry.get(ch))

	assert.Error(t, entry.set(ch, "invalid"))
}

func TestWeComFields_DMPolicy(t *testing.T) {
	entry := wecomFields["dm_policy"]
	ch := &onboard.WeComChannelConfig{}

	for _, v := range []string{"open", "pairing", "allowlist"} {
		require.NoError(t, entry.set(ch, v))
		assert.Equal(t, v, entry.get(ch))
	}
	assert.Error(t, entry.set(ch, "closed"))
}

func TestWeComFields_AllowFrom(t *testing.T) {
	entry := wecomFields["allow_from"]
	ch := &onboard.WeComChannelConfig{}

	require.NoError(t, entry.set(ch, "alice, bob"))
	assert.Equal(t, []string{"alice", "bob"}, ch.AllowFrom)
	assert.Equal(t, "alice,bob", entry.get(ch))
}

func TestWeComFields_GroupPolicy(t *testing.T) {
	entry := wecomFields["group_policy"]
	ch := &onboard.WeComChannelConfig{}

	for _, v := range []string{"open", "allowlist", "disabled"} {
		require.NoError(t, entry.set(ch, v))
		assert.Equal(t, v, entry.get(ch))
	}
	assert.Error(t, entry.set(ch, "invite"))
}

func TestWeComFields_GroupAllowFrom(t *testing.T) {
	entry := wecomFields["group_allow_from"]
	ch := &onboard.WeComChannelConfig{}

	require.NoError(t, entry.set(ch, "g1,g2"))
	assert.Equal(t, []string{"g1", "g2"}, ch.GroupAllowFrom)
	assert.Equal(t, "g1,g2", entry.get(ch))
}

func TestWeComFields_Groups(t *testing.T) {
	entry := wecomFields["groups"]
	ch := &onboard.WeComChannelConfig{}

	assert.Equal(t, "", entry.get(ch))

	validJSON := `{"room1":{"enabled":true,"allow_from":["alice"]}}`
	require.NoError(t, entry.set(ch, validJSON))
	assert.NotEmpty(t, entry.get(ch))

	assert.Error(t, entry.set(ch, "not json"))
}

func TestWeComFields_ConnectionMode(t *testing.T) {
	entry := wecomFields["connection_mode"]
	ch := &onboard.WeComChannelConfig{}

	require.NoError(t, entry.set(ch, "webhook"))
	assert.Equal(t, "webhook", entry.get(ch))

	require.NoError(t, entry.set(ch, "websocket"))
	assert.Equal(t, "websocket", entry.get(ch))

	assert.Error(t, entry.set(ch, "grpc"))
}

func TestWeComFields_BotID(t *testing.T) {
	entry := wecomFields["botid"]
	ch := &onboard.WeComChannelConfig{}

	require.NoError(t, entry.set(ch, "bot123"))
	assert.Equal(t, "bot123", entry.get(ch))
}

func TestWeComFields_Secret(t *testing.T) {
	entry := wecomFields["secret"]
	ch := &onboard.WeComChannelConfig{}

	assert.True(t, entry.secret)
	require.NoError(t, entry.set(ch, "s3cret"))
	assert.Equal(t, "s3cret", entry.get(ch))
}

func TestWeComFields_TextChunkLimit(t *testing.T) {
	entry := wecomFields["text_chunk_limit"]
	ch := &onboard.WeComChannelConfig{}

	require.NoError(t, entry.set(ch, "2048"))
	assert.Equal(t, 2048, ch.TextChunkLimit)
	assert.Equal(t, "2048", entry.get(ch))

	assert.Error(t, entry.set(ch, "xyz"))
}

// ── configKeys getter/setter tests ──

func TestConfigKeys_CodexWorkDir(t *testing.T) {
	entry := configKeys["codex.workdir"]
	cfg := &onboard.FileConfig{}

	require.NoError(t, entry.set(cfg, "/tmp/work"))
	assert.Equal(t, "/tmp/work", entry.get(cfg))
}

func TestConfigKeys_CodexTimeout(t *testing.T) {
	entry := configKeys["codex.timeout"]
	cfg := &onboard.FileConfig{}

	require.NoError(t, entry.set(cfg, "30m"))
	assert.Equal(t, "30m", entry.get(cfg))
}

func TestConfigKeys_CodexSandbox(t *testing.T) {
	entry := configKeys["codex.sandbox"]
	cfg := &onboard.FileConfig{}

	for _, v := range []string{
		"read-only", "workspace-write", "danger-full-access",
	} {
		require.NoError(t, entry.set(cfg, v))
		assert.Equal(t, v, entry.get(cfg))
	}
	assert.Error(t, entry.set(cfg, "none"))
}

func TestConfigKeys_CodexGroupSandbox(t *testing.T) {
	entry := configKeys["codex.group_sandbox"]
	cfg := &onboard.FileConfig{}

	for _, v := range []string{
		"read-only", "workspace-write", "danger-full-access",
	} {
		require.NoError(t, entry.set(cfg, v))
		assert.Equal(t, v, entry.get(cfg))
	}
	assert.Error(t, entry.set(cfg, "off"))
}

func TestConfigKeys_GatewayAddress(t *testing.T) {
	entry := configKeys["gateway.address"]
	cfg := &onboard.FileConfig{}

	require.NoError(t, entry.set(cfg, ":9090"))
	assert.Equal(t, ":9090", entry.get(cfg))
}

func TestConfigKeys_LoggingLevel(t *testing.T) {
	entry := configKeys["logging.level"]
	cfg := &onboard.FileConfig{}

	for _, v := range []string{"debug", "info", "warn", "error"} {
		require.NoError(t, entry.set(cfg, v))
		assert.Equal(t, v, entry.get(cfg))
	}
	assert.Error(t, entry.set(cfg, "trace"))
}

func TestConfigKeys_LoggingFormat(t *testing.T) {
	entry := configKeys["logging.format"]
	cfg := &onboard.FileConfig{}

	for _, v := range []string{"text", "json"} {
		require.NoError(t, entry.set(cfg, v))
		assert.Equal(t, v, entry.get(cfg))
	}
	assert.Error(t, entry.set(cfg, "yaml"))
}

func TestConfigKeys_LoggingCodexFile(t *testing.T) {
	entry := configKeys["logging.codex_file"]
	cfg := &onboard.FileConfig{}

	require.NoError(t, entry.set(cfg, "/tmp/codex.log"))
	assert.Equal(t, "/tmp/codex.log", entry.get(cfg))
}

func TestConfigKeys_MetaVersionReadOnly(t *testing.T) {
	entry := configKeys["meta.version"]
	cfg := &onboard.FileConfig{
		Meta: onboard.MetaFileConfig{
			LastTouchedVersion: "1.0.0",
		},
	}
	assert.Equal(t, "1.0.0", entry.get(cfg))
	assert.Error(t, entry.set(cfg, "2.0.0"))
}

func TestConfigKeys_MetaTouchedAtReadOnly(t *testing.T) {
	entry := configKeys["meta.touched_at"]
	cfg := &onboard.FileConfig{
		Meta: onboard.MetaFileConfig{
			LastTouchedAt: "2025-01-01T00:00:00Z",
		},
	}
	assert.Equal(t, "2025-01-01T00:00:00Z", entry.get(cfg))
	assert.Error(t, entry.set(cfg, "now"))
}

// ── configSetChannelWildcard tests ──

func TestConfigSetChannelWildcard_NoChannels(t *testing.T) {
	cfg := &onboard.FileConfig{}
	err := configSetChannelWildcard(cfg, "dm_policy", "open")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no channels configured")
}

func TestConfigSetChannelWildcard_UnknownField(t *testing.T) {
	cfg := &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": mustMarshalConfig(t, onboard.TelegramChannelConfig{
				Type: "telegram",
			}),
		},
	}
	err := configSetChannelWildcard(cfg, "nonexistent", "val")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown channel field")
}

func TestConfigSetChannelWildcard_UpdatesTelegram(t *testing.T) {
	// configSetChannelWildcard calls SaveFileConfig internally,
	// which requires filesystem. We test the field matching
	// logic through telegramFields directly here.
	entry := telegramFields["dm_policy"]
	ch1 := &onboard.TelegramChannelConfig{Type: "telegram"}
	require.NoError(t, entry.set(ch1, "open"))
	assert.Equal(t, "open", ch1.DMPolicy)
}

func TestConfigSetChannelWildcard_IncompatibleValue(t *testing.T) {
	// Verify that setting an int field with a non-int value on
	// all channels returns an error.
	entry := telegramFields["text_chunk_limit"]
	ch := &onboard.TelegramChannelConfig{Type: "telegram"}
	err := entry.set(ch, "not_a_number")
	assert.Error(t, err)
}

// ── ConfigSet / ConfigGet / ConfigList / ConfigFile integration tests ──
// These tests use t.Setenv("HOME", ...) to isolate filesystem side effects.

func setupTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func writeTestConfig(t *testing.T, cfg *onboard.FileConfig) {
	t.Helper()
	path, err := onboard.ConfigPath()
	require.NoError(t, err)
	onboard.SaveFileConfigTo(cfg, path)
}

func TestConfigSet_StaticKey(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigSet("codex.workdir", "/tmp/test-workdir")
	require.NoError(t, err)

	// Verify via get.
	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/test-workdir", cfg.Codex.WorkDir)
}

func TestConfigSet_ValidatedKey(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigSet("logging.level", "debug")
	require.NoError(t, err)

	// Invalid value.
	err = ConfigSet("logging.level", "trace")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid value")
}

func TestConfigSet_UnknownKey(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigSet("nonexistent.key", "val")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
}

func TestConfigSet_ChannelKey_Telegram(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": mustMarshalConfig(t, onboard.TelegramChannelConfig{
				Type: "telegram",
			}),
		},
	})

	err := ConfigSet("channels.tg.dm_policy", "open")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	var ch onboard.TelegramChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["tg"], &ch))
	assert.Equal(t, "open", ch.DMPolicy)
}

func TestConfigSet_ChannelKey_WeCom(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc": mustMarshalConfig(t, onboard.WeComChannelConfig{
				Type: "wecom",
			}),
		},
	})

	err := ConfigSet("channels.wc.webhook_path", "/wc/hook")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	var ch onboard.WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc"], &ch))
	assert.Equal(t, "/wc/hook", ch.WebhookPath)
}

func TestConfigSet_ChannelKey_NewChannel(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	// Setting a field on a non-existing channel should create it.
	err := ConfigSet("channels.newtg.bot_token", "tok123")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	require.Contains(t, cfg.Channels, "newtg")
	var ch onboard.TelegramChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["newtg"], &ch))
	assert.Equal(t, "tok123", ch.BotToken)
	assert.Equal(t, "telegram", ch.Type)
}

func TestConfigSet_ChannelKey_InvalidField(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": mustMarshalConfig(t, onboard.TelegramChannelConfig{
				Type: "telegram",
			}),
		},
	})

	err := ConfigSet("channels.tg.nonexistent_field", "val")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown telegram field")
}

func TestConfigSet_ChannelKey_WildcardSetsAll(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg1": mustMarshalConfig(t, onboard.TelegramChannelConfig{
				Type: "telegram",
			}),
			"tg2": mustMarshalConfig(t, onboard.TelegramChannelConfig{
				Type: "telegram",
			}),
		},
	})

	err := ConfigSet("channels.*.dm_policy", "open")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	for _, name := range []string{"tg1", "tg2"} {
		var ch onboard.TelegramChannelConfig
		require.NoError(t, json.Unmarshal(cfg.Channels[name], &ch))
		assert.Equal(t, "open", ch.DMPolicy, "channel %s", name)
	}
}

func TestConfigGet_StaticKey(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{WorkDir: "/work"},
	})

	err := ConfigGet("codex.workdir")
	require.NoError(t, err)
}

func TestConfigGet_ChannelKey(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": mustMarshalConfig(t, onboard.TelegramChannelConfig{
				Type:     "telegram",
				DMPolicy: "pairing",
			}),
		},
	})

	err := ConfigGet("channels.tg.dm_policy")
	require.NoError(t, err)
}

func TestConfigGet_ChannelNotExist(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigGet("channels.missing.dm_policy")
	require.NoError(t, err) // prints "(not set)"
}

func TestConfigGet_UnknownKey(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigGet("nonexistent.key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
}

func TestConfigGet_Wildcard(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg1": mustMarshalConfig(t, onboard.TelegramChannelConfig{
				Type:     "telegram",
				DMPolicy: "open",
			}),
			"wc1": mustMarshalConfig(t, onboard.WeComChannelConfig{
				Type:     "wecom",
				DMPolicy: "pairing",
			}),
		},
	})

	err := ConfigGet("channels.*.dm_policy")
	require.NoError(t, err)
}

func TestConfigGet_WildcardNoChannels(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigGet("channels.*.dm_policy")
	require.NoError(t, err) // prints "(no channels configured)"
}

func TestConfigGet_WildcardUnknownField(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": mustMarshalConfig(t, onboard.TelegramChannelConfig{Type: "telegram"}),
		},
	})

	err := ConfigGet("channels.*.bad_field")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown channel field")
}

func TestConfigList(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Codex: onboard.CodexFileConfig{WorkDir: "/work"},
		Channels: map[string]json.RawMessage{
			"tg": mustMarshalConfig(t, onboard.TelegramChannelConfig{
				Type:     "telegram",
				DMPolicy: "open",
			}),
			"wc": mustMarshalConfig(t, onboard.WeComChannelConfig{
				Type:     "wecom",
				DMPolicy: "pairing",
			}),
		},
	})

	err := ConfigList()
	require.NoError(t, err)
}

func TestConfigList_Empty(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigList()
	require.NoError(t, err)
}

func TestConfigFile(t *testing.T) {
	setupTestHome(t)

	err := ConfigFile()
	require.NoError(t, err)
}

// ── configSetChannel edge cases ──

func TestConfigSetChannel_InvalidKeyFormat(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := configSetChannel("channels.", "val")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid channel key format")
}

func TestConfigSetChannel_AmbiguousField(t *testing.T) {
	// dm_policy exists in both telegram and wecom — test that it works
	// when channel already has a type.
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc": mustMarshalConfig(t, onboard.WeComChannelConfig{
				Type: "wecom",
			}),
		},
	})

	err := configSetChannel("channels.wc.dm_policy", "open")
	require.NoError(t, err)
}

func TestConfigSetChannel_UnknownChannelType(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"slack": json.RawMessage(`{"type":"slack"}`),
		},
	})

	err := configSetChannel("channels.slack.dm_policy", "open")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown channel type")
}

// ── configGetChannel edge cases ──

func TestConfigGetChannel_InvalidKeyFormat(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := configGetChannel("channels.")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid channel key format")
}

func TestConfigGetChannel_UnknownChannelType(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"slack": json.RawMessage(`{"type":"slack"}`),
		},
	})

	err := configGetChannel("channels.slack.dm_policy")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown channel type")
}

func TestConfigGetChannel_UnknownField(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg": mustMarshalConfig(t, onboard.TelegramChannelConfig{Type: "telegram"}),
		},
	})

	err := configGetChannel("channels.tg.nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown telegram field")
}

// ── resolveCodexTracePath additional tests ──

func TestResolveCodexTracePath_Absolute(t *testing.T) {
	path, enabled, err := resolveCodexTracePath("/var/log/codex.log", "/tmp/data")
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, "/var/log/codex.log", path)
}

func TestResolveCodexTracePath_None(t *testing.T) {
	path, enabled, err := resolveCodexTracePath("none", "/tmp/data")
	require.NoError(t, err)
	assert.False(t, enabled)
	assert.Empty(t, path)
}

func TestResolveCodexTracePath_Whitespace(t *testing.T) {
	path, enabled, err := resolveCodexTracePath("  ", "/tmp/data")
	require.NoError(t, err)
	assert.True(t, enabled)
	assert.Equal(t, "/tmp/data/codex.log", path)
}

func TestResolveCodexTracePath_RelativeNoDataDir(t *testing.T) {
	_, _, err := resolveCodexTracePath("relative.log", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing data directory")
}

// ── openCodexTraceLogger tests ──

func TestOpenCodexTraceLogger_Disabled(t *testing.T) {
	logger, file, path, err := openCodexTraceLogger("off", t.TempDir())
	require.NoError(t, err)
	assert.Nil(t, logger)
	assert.Nil(t, file)
	assert.Empty(t, path)
}

func TestOpenCodexTraceLogger_Default(t *testing.T) {
	dataDir := t.TempDir()
	logger, file, path, err := openCodexTraceLogger("", dataDir)
	require.NoError(t, err)
	require.NotNil(t, logger)
	require.NotNil(t, file)
	defer file.Close()
	assert.Contains(t, path, "codex.log")
}

func TestOpenCodexTraceLogger_CustomPath(t *testing.T) {
	dataDir := t.TempDir()
	logger, file, path, err := openCodexTraceLogger("custom/trace.log", dataDir)
	require.NoError(t, err)
	require.NotNil(t, logger)
	require.NotNil(t, file)
	defer file.Close()
	assert.Contains(t, path, "custom/trace.log")
}

// ── configSetChannelWildcard additional tests ──

func TestConfigSetChannelWildcard_MixedTypes(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg1": mustMarshalConfig(t, onboard.TelegramChannelConfig{
				Type: "telegram",
			}),
			"wc1": mustMarshalConfig(t, onboard.WeComChannelConfig{
				Type: "wecom",
			}),
		},
	})

	// dm_policy is shared by both types.
	err := ConfigSet("channels.*.dm_policy", "open")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)

	var tg onboard.TelegramChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["tg1"], &tg))
	assert.Equal(t, "open", tg.DMPolicy)

	var wc onboard.WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc1"], &wc))
	assert.Equal(t, "open", wc.DMPolicy)
}

func TestConfigSetChannelWildcard_SkipsIncompatible(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg1": mustMarshalConfig(t, onboard.TelegramChannelConfig{
				Type: "telegram",
			}),
		},
	})

	// text_chunk_limit with invalid value for all channels.
	err := ConfigSet("channels.*.text_chunk_limit", "not_a_number")
	assert.Error(t, err)
}

func TestConfigSetChannelWildcard_NoMatchingField(t *testing.T) {
	setupTestHome(t)
	// Only have wecom channels, but try to set a telegram-only field.
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc1": mustMarshalConfig(t, onboard.WeComChannelConfig{
				Type: "wecom",
			}),
		},
	})

	// "require_mention" only exists in telegram, not wecom.
	err := ConfigSet("channels.*.require_mention", "true")
	assert.Error(t, err)
}

// ── helper ──

func mustMarshalConfig(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

// ── Additional coverage: configGetChannel wecom, configSetChannel wecom fields ──

func TestConfigSetChannel_WeComToken(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc1": mustMarshalConfig(t, onboard.WeComChannelConfig{Type: "wecom"}),
		},
	})

	err := ConfigSet("channels.wc1.token", "new-token-val")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	var ch onboard.WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc1"], &ch))
	assert.Equal(t, "new-token-val", ch.Token)
}

func TestConfigSetChannel_WeComConnectionMode(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc2": mustMarshalConfig(t, onboard.WeComChannelConfig{Type: "wecom"}),
		},
	})

	err := ConfigSet("channels.wc2.connection_mode", "websocket")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	var ch onboard.WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc2"], &ch))
	assert.Equal(t, "websocket", ch.ConnectionMode)
}

func TestConfigSetChannel_WeComGroupPolicy(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc3": mustMarshalConfig(t, onboard.WeComChannelConfig{Type: "wecom"}),
		},
	})

	err := ConfigSet("channels.wc3.group_policy", "open")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	var ch onboard.WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc3"], &ch))
	assert.Equal(t, "open", ch.GroupPolicy)
}

func TestConfigSetChannel_WeComAllowFrom(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc4": mustMarshalConfig(t, onboard.WeComChannelConfig{Type: "wecom"}),
		},
	})

	err := ConfigSet("channels.wc4.allow_from", "alice,bob,charlie")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	var ch onboard.WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc4"], &ch))
	assert.Equal(t, []string{"alice", "bob", "charlie"}, ch.AllowFrom)
}

func TestConfigGetChannel_WeComField(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc5": mustMarshalConfig(t, onboard.WeComChannelConfig{
				Type:     "wecom",
				DMPolicy: "open",
				Token:    "secret-tok",
			}),
		},
	})

	err := ConfigGet("channels.wc5.dm_policy")
	require.NoError(t, err)
}

func TestConfigGetChannel_NotSet(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigGet("channels.nonexist.dm_policy")
	require.NoError(t, err)
}

func TestConfigGetChannelWildcard_WeComField(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc-a": mustMarshalConfig(t, onboard.WeComChannelConfig{
				Type:     "wecom",
				DMPolicy: "allowlist",
			}),
			"wc-b": mustMarshalConfig(t, onboard.WeComChannelConfig{
				Type:     "wecom",
				DMPolicy: "open",
			}),
		},
	})

	err := ConfigGet("channels.*.dm_policy")
	require.NoError(t, err)
}

func TestConfigGetChannelWildcard_NoChannels(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigGet("channels.*.dm_policy")
	require.NoError(t, err)
}

func TestConfigGetChannelWildcard_UnknownField(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"tg1": mustMarshalConfig(t, onboard.TelegramChannelConfig{Type: "telegram"}),
		},
	})

	err := ConfigGet("channels.*.nonexistent_field")
	assert.Error(t, err)
}

func TestConfigSetChannel_NewWeComChannel(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	// Creating a new channel inferred from field type
	err := ConfigSet("channels.new-wc.token", "my-token")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	var ch onboard.WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["new-wc"], &ch))
	assert.Equal(t, "my-token", ch.Token)
	assert.Equal(t, "wecom", ch.Type)
}

func TestConfigSetChannel_NewTelegramChannel(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigSet("channels.new-tg.bot_token", "123:ABC")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	var ch onboard.TelegramChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["new-tg"], &ch))
	assert.Equal(t, "123:ABC", ch.BotToken)
	assert.Equal(t, "telegram", ch.Type)
}

func TestConfigSet_UnknownKeyError(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigSet("totally.unknown.key", "val")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
}

func TestConfigGet_UnknownKeyError(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigGet("totally.unknown.key")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown config key")
}

func TestConfigSetChannel_UnknownField(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigSet("channels.test.nonexistent_field", "val")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown channel field")
}

func TestConfigSetChannel_WeComTextChunkLimit(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc6": mustMarshalConfig(t, onboard.WeComChannelConfig{Type: "wecom"}),
		},
	})

	err := ConfigSet("channels.wc6.text_chunk_limit", "2000")
	require.NoError(t, err)

	cfg, err := onboard.LoadFileConfig()
	require.NoError(t, err)
	var ch onboard.WeComChannelConfig
	require.NoError(t, json.Unmarshal(cfg.Channels["wc6"], &ch))
	assert.Equal(t, 2000, ch.TextChunkLimit)
}

func TestConfigSetChannel_WeComTextChunkLimitInvalid(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc7": mustMarshalConfig(t, onboard.WeComChannelConfig{Type: "wecom"}),
		},
	})

	err := ConfigSet("channels.wc7.text_chunk_limit", "not-a-number")
	assert.Error(t, err)
}

func TestConfigSetChannelWildcard_NoChannelsError(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{})

	err := ConfigSet("channels.*.dm_policy", "open")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no channels configured")
}

func TestConfigSetChannel_WeComGroups(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc8": mustMarshalConfig(t, onboard.WeComChannelConfig{Type: "wecom"}),
		},
	})

	err := ConfigSet("channels.wc8.groups", `{"grp1":{"enabled":true}}`)
	require.NoError(t, err)
}

func TestConfigSetChannel_WeComGroupsInvalidJSON(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, &onboard.FileConfig{
		Channels: map[string]json.RawMessage{
			"wc9": mustMarshalConfig(t, onboard.WeComChannelConfig{Type: "wecom"}),
		},
	})

	err := ConfigSet("channels.wc9.groups", "not-json")
	assert.Error(t, err)
}
