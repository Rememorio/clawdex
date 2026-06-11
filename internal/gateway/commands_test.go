package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/codex"
)

func newTestClient(t *testing.T) *codex.Client {
	t.Helper()
	workDir := filepath.Join(t.TempDir(), "workspace")
	require.NoError(t, os.MkdirAll(workDir, 0o755))
	store := codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
	return &codex.Client{
		Store:   store,
		Sandbox: "workspace-write",
		WorkDir: workDir,
		Timeout: 20 * time.Minute,
	}
}

func TestHandleCommand_Help(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/help"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "/help")
	assert.Contains(t, resp.text, "/new")
	assert.Contains(t, resp.text, "/status")
	assert.Contains(t, resp.text, "/sessions")
	assert.Contains(t, resp.text, "/resume")
	assert.Contains(t, resp.text, "/cron — Manage scheduled jobs")
	assert.Contains(t, resp.text, "/cron list")
	assert.Contains(t, resp.text, "/cron status <id|index|name>")
	assert.Less(t, strings.Index(resp.text, "/cron —"), strings.Index(resp.text, "/cancel —"))
	assert.True(t, resp.textOnly)
}

func TestHandleCommand_Help_AlwaysHasKeyboard(t *testing.T) {
	c := newTestClient(t)

	// All channels get keyboard data; dispatch to KeyboardResponder is
	// decided at the gateway level based on the responder interface.
	for _, ch := range []string{"telegram", "wecom", "wecom-ai", "slack", ""} {
		msg := channel.Message{Channel: ch, ChatID: 1, Text: "/help"}
		resp, ok := handleCommand(c, msg)
		assert.True(t, ok, "channel=%q", ch)
		require.NotNil(t, resp.keyboard, "channel=%q should have keyboard", ch)
		require.Len(t, resp.keyboard, 1, "channel=%q", ch)
		require.Len(t, resp.keyboard[0], 3, "channel=%q", ch)
		assert.Equal(t, "/new", resp.keyboard[0][0].callbackData, "channel=%q", ch)
		assert.Equal(t, "/sessions", resp.keyboard[0][1].callbackData, "channel=%q", ch)
		assert.Equal(t, "/status", resp.keyboard[0][2].callbackData, "channel=%q", ch)
	}
}

func TestHandleCommand_New(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/new"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Session cleared")
	assert.Nil(t, resp.keyboard)

	// Should have a session card.
	require.NotNil(t, resp.sessionCard)
	assert.Equal(t, "New Session", resp.sessionCard.Title)
	require.Len(t, resp.sessionCard.Buttons, 2)
}

func TestHandleCommand_Help_HasSessionCard(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/help"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	require.NotNil(t, resp.sessionCard)
	assert.Equal(t, "Help", resp.sessionCard.Title)
	assert.Equal(t, "Available commands", resp.sessionCard.Desc)
	assert.Contains(t, resp.sessionCard.Body, "/cron — Manage scheduled jobs")
	assert.Contains(t, resp.sessionCard.Body, "/cron list")
	require.Len(t, resp.sessionCard.Buttons, 2)
	assert.Equal(t, "/sessions", resp.sessionCard.Buttons[0].Text)
	assert.Equal(t, "/status", resp.sessionCard.Buttons[1].Text)
	assert.True(t, resp.textOnly)
}

func TestHandleCommand_Help_GroupNoCard(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/help", ChatType: "group"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Nil(t, resp.sessionCard, "group chat should not get a session card for help")
}

func TestHandleCommand_Status_NoSession(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/status"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "clawdex chat status")
	assert.Contains(t, resp.text, "Scope:    private chat")
	assert.Contains(t, resp.text, "Session:  none")
	assert.Contains(t, resp.text, "SOUL.md:  not configured")
	// Server-internal fields must not be exposed.
	assert.NotContains(t, resp.text, "Sandbox:")
	assert.NotContains(t, resp.text, "Workdir:")
	assert.NotContains(t, resp.text, "Timeout:")

	// Should have a session card.
	require.NotNil(t, resp.sessionCard)
	assert.Equal(t, "Status", resp.sessionCard.Title)
	assert.Equal(t, "Current chat context", resp.sessionCard.Desc)
	assert.Contains(t, resp.sessionCard.Body, "clawdex chat status")
	require.Len(t, resp.sessionCard.Buttons, 2)
	assert.Equal(t, "/sessions", resp.sessionCard.Buttons[0].Text)
}

func TestHandleCommand_Status_WithSession(t *testing.T) {
	c := newTestClient(t)
	c.SetSession(1, "sess-abc-123-def-456-ghi-789-jkl-01234")
	msg := channel.Message{ChatID: 1, Text: "/status"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Session:  `sess-abc")
}

func TestHandleCommand_Status_WithSoul(t *testing.T) {
	c := &codex.Client{
		SoulContent: "You are a helpful cat.",
	}
	msg := channel.Message{ChatID: 1, Text: "/status"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "SOUL.md:  loaded (global)")
}

func TestHandleCommand_Status_GroupContext(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{
		Channel:  "telegram-main",
		ChatID:   1,
		Text:     "/status",
		ChatType: "group",
	}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Channel:  telegram-main")
	assert.Contains(t, resp.text, "Scope:    group chat (shared session)")
}

func TestHandleCommand_Status_WithChannelSoulOverride(t *testing.T) {
	c := &codex.Client{
		SoulContent: "global soul",
		SoulOverrides: map[string]string{
			"telegram-main": "channel soul",
		},
	}
	msg := channel.Message{Channel: "telegram-main", ChatID: 1, Text: "/status"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "SOUL.md:  loaded (channel override)")
}

func TestHandleCommand_Unknown(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "hello world"}

	_, ok := handleCommand(c, msg)
	assert.False(t, ok)
}

func TestHandleCommand_UnknownSlash(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/unknown"}

	_, ok := handleCommand(c, msg)
	assert.False(t, ok)
}

func TestHandleCommand_WhitespaceAroundCommand(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "  /help  "}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "/help")
}

// ── /sessions tests ──

func TestHandleCommand_Sessions_WithData(t *testing.T) {
	c := newTestClient(t)

	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "你是谁呀")
	time.Sleep(10 * time.Millisecond)
	c.Store.Activate(1, "019cc76f-aaaa-bbbb-cccc-dddddddddddd", "hello")

	msg := channel.Message{Channel: "telegram", ChatID: 1, Text: "/sessions"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Recent sessions:")
	assert.Contains(t, resp.text, "你是谁呀")
	assert.Contains(t, resp.text, "019cc781")
	assert.Contains(t, resp.text, "hello")
	assert.Contains(t, resp.text, "019cc76f")
	assert.Contains(t, resp.text, "Tap a button or use")
	require.Len(t, resp.keyboard, 2)
	assert.Contains(t, resp.keyboard[0][0].callbackData, "/resume ")
	assert.Contains(t, resp.keyboard[1][0].callbackData, "/resume ")
}

func TestHandleCommand_Sessions_AnyChannelHasKeyboard(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "test session")

	for _, ch := range []string{"wecom", "wecom-ai", "slack", ""} {
		msg := channel.Message{Channel: ch, ChatID: 1, Text: "/sessions"}
		resp, ok := handleCommand(c, msg)
		assert.True(t, ok, "channel=%q", ch)
		assert.Contains(t, resp.text, "Tap a button or use", "channel=%q", ch)
		require.NotNil(t, resp.keyboard, "channel=%q should have keyboard", ch)
		require.Len(t, resp.keyboard, 1, "channel=%q", ch)
		assert.Contains(t, resp.keyboard[0][0].callbackData, "/resume ", "channel=%q", ch)
	}
}

func TestHandleCommand_Sessions_Isolated(t *testing.T) {
	c := newTestClient(t)

	c.Store.Activate(1, "thread-chat1", "chat 1 session")
	c.Store.Activate(2, "thread-chat2", "chat 2 session")

	// Chat 1 should only see its own session.
	msg := channel.Message{ChatID: 1, Text: "/sessions"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "thread-c")
	assert.NotContains(t, resp.text, "chat 2 session")

	// Chat 2 should only see its own session.
	msg = channel.Message{ChatID: 2, Text: "/sessions"}
	resp, ok = handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "chat 2 session")
	assert.NotContains(t, resp.text, "chat 1 session")
}

func TestHandleCommand_Sessions_Empty(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/sessions"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Equal(t, "No sessions found.", resp.text)
}

func TestHandleCommand_Sessions_NoStore(t *testing.T) {
	c := &codex.Client{}
	msg := channel.Message{ChatID: 1, Text: "/sessions"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Equal(t, "No sessions found.", resp.text)
}

// ── /resume tests ──

func TestHandleCommand_Resume_FullID(t *testing.T) {
	c := newTestClient(t)
	fullID := "019cc781-6b9f-7362-ad33-8fc36f7661dd"
	msg := channel.Message{ChatID: 42, Text: "/resume " + fullID}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Switched to session")
	assert.Contains(t, resp.text, fullID)

	// Verify session was actually set.
	assert.Equal(t, fullID, c.GetSessionID(42))
}

func TestHandleCommand_Resume_Prefix(t *testing.T) {
	c := newTestClient(t)
	fullID := "019cc781-6b9f-7362-ad33-8fc36f7661dd"
	c.Store.Activate(42, fullID, "test")

	msg := channel.Message{ChatID: 42, Text: "/resume 019cc781"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Switched to session")
	assert.Contains(t, resp.text, fullID)
	assert.Equal(t, fullID, c.GetSessionID(42))
}

func TestHandleCommand_Resume_NoArgs(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/resume"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "No sessions to resume")
}

func TestHandleCommand_Resume_NoArgs_WithSessions(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "test session")
	msg := channel.Message{Channel: "telegram", ChatID: 1, Text: "/resume"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Recent sessions:")
	require.NotEmpty(t, resp.keyboard)
	assert.Contains(t, resp.keyboard[0][0].callbackData, "/resume 019cc781-6b9f-7362-ad33-8fc36f7661dd")
}

func TestHandleCommand_Resume_NotFound(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/resume nonexistent"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "No session found")
}

func TestHandleCommand_Resume_Ambiguous(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "abc-111-aaa", "s1")
	c.Store.Activate(1, "abc-222-bbb", "s2")

	msg := channel.Message{ChatID: 1, Text: "/resume abc"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Ambiguous")
}

// ── relativeTime tests ──

func TestRelativeTime(t *testing.T) {
	assert.Equal(t, "just now", relativeTime(time.Now()))
	assert.Equal(t, "5m ago", relativeTime(time.Now().Add(-5*time.Minute)))
	assert.Equal(t, "2h ago", relativeTime(time.Now().Add(-2*time.Hour)))
	assert.Equal(t, "3d ago", relativeTime(time.Now().Add(-3*24*time.Hour)))
	assert.Equal(t, "", relativeTime(time.Time{}))
}

// ── /resume with whitespace ──

func TestHandleCommand_Resume_WithExtraWhitespace(t *testing.T) {
	c := newTestClient(t)
	fullID := "019cc781-6b9f-7362-ad33-8fc36f7661dd"
	msg := channel.Message{ChatID: 42, Text: "  /resume   " + fullID + "  "}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Switched to session")
}

func TestHandleCommand_Resume_PrefixWithInvisibleSuffix(t *testing.T) {
	c := newTestClient(t)
	fullID := "019cc781-6b9f-7362-ad33-8fc36f7661dd"
	c.Store.Activate(42, fullID, "test")
	msg := channel.Message{ChatID: 42, Text: "/resume 019cc781" + "\u2060"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Switched to session")
	assert.Contains(t, resp.text, fullID)
	assert.Equal(t, fullID, c.GetSessionID(42))
}

// ── Group chat command restriction tests ──

func TestHandleCommand_GroupDisabled(t *testing.T) {
	c := newTestClient(t)

	for _, cmd := range []string{"/new", "/sessions", "/resume abc"} {
		msg := channel.Message{ChatID: 1, Text: cmd, ChatType: "group"}
		resp, ok := handleCommand(c, msg)
		assert.True(t, ok, "command %s should be handled", cmd)
		assert.Contains(t, resp.text, "not available in group chats", "command %s should be blocked in group", cmd)
	}
}

func TestHandleCommand_GroupAllowed(t *testing.T) {
	c := newTestClient(t)

	for _, cmd := range []string{"/help", "/status"} {
		msg := channel.Message{ChatID: 1, Text: cmd, ChatType: "group"}
		resp, ok := handleCommand(c, msg)
		assert.True(t, ok, "command %s should be handled", cmd)
		assert.NotContains(t, resp.text, "not available in group chats", "command %s should work in group", cmd)
	}
}

func TestHandleCommand_Help_GroupFiltersCommands(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{Channel: "telegram", ChatID: 1, Text: "/help", ChatType: "group"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)

	// Should contain /help and /status.
	assert.Contains(t, resp.text, "/help")
	assert.Contains(t, resp.text, "/status")

	// Should NOT contain group-disabled commands.
	assert.NotContains(t, resp.text, "/new")
	assert.NotContains(t, resp.text, "/sessions")
	assert.NotContains(t, resp.text, "/resume")

	// Keyboard should only have /status (not /new or /sessions).
	require.NotNil(t, resp.keyboard)
	require.Len(t, resp.keyboard, 1)
	require.Len(t, resp.keyboard[0], 1)
	assert.Equal(t, "/status", resp.keyboard[0][0].callbackData)
}

// ── /sessions session card tests ──

func TestHandleCommand_Sessions_HasSessionCard(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "First chat")
	time.Sleep(10 * time.Millisecond)
	c.Store.Activate(1, "019cc76f-aaaa-bbbb-cccc-dddddddddddd", "Second chat")

	msg := channel.Message{Channel: "wecom", ChatID: 1, Text: "/sessions"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)

	// Should have a session card.
	require.NotNil(t, resp.sessionCard)
	assert.Equal(t, "🧵 Sessions", resp.sessionCard.Title)
	assert.Contains(t, resp.sessionCard.Desc, "2 session(s)")
	assert.Contains(t, resp.sessionCard.Body, "First chat")
	assert.Contains(t, resp.sessionCard.Body, "Second chat")

	// Dropdown options.
	require.Len(t, resp.sessionCard.Sessions, 2)
	assert.Contains(t, resp.sessionCard.Sessions[0].Label, "019cc76f")
	assert.Contains(t, resp.sessionCard.Sessions[1].Label, "019cc781")

	// Buttons.
	require.Len(t, resp.sessionCard.Buttons, 2)
	assert.Equal(t, "/resume", resp.sessionCard.Buttons[0].Text)
	assert.Equal(t, "/new", resp.sessionCard.Buttons[1].Text)
}

func TestHandleCommand_Sessions_NoCardWhenEmpty(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/sessions"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Nil(t, resp.sessionCard, "no card when there are no sessions")
}

func TestHandleCommand_Resume_NoArgs_HasSessionCard(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "test session")
	msg := channel.Message{Channel: "wecom", ChatID: 1, Text: "/resume"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "Recent sessions:")
	require.NotNil(t, resp.sessionCard)
	require.Len(t, resp.sessionCard.Sessions, 1)
}

func TestHandleCommand_Resume_NoArgs_NoCardWhenEmpty(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/resume"}

	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Nil(t, resp.sessionCard)
}

// ── buildSessionsCard tests ──

func TestBuildSessionsCard_Basic(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "你是谁呀")
	time.Sleep(10 * time.Millisecond)
	c.Store.Activate(1, "019cc76f-aaaa-bbbb-cccc-dddddddddddd", "hello")

	sessions := c.Store.List(1, 10)
	currentThreadID := c.GetSessionID(1)

	card := buildSessionsCard(currentThreadID, sessions)

	assert.Equal(t, "🧵 Sessions", card.Title)
	assert.Contains(t, card.Desc, "Current: 019cc76f")
	assert.Contains(t, card.Desc, "2 session(s)")
	assert.Contains(t, card.Body, "hello")
	assert.Contains(t, card.Body, "你是谁呀")
	assert.Contains(t, card.Body, "Select from dropdown")

	require.Len(t, card.Sessions, 2)
	assert.Equal(t, "019cc76f-aaaa-bbbb-cccc-dddddddddddd", card.Sessions[0].ID)
	assert.Contains(t, card.Sessions[0].Label, "hello")
	assert.Contains(t, card.Sessions[0].Label, "✓")

	assert.Equal(t, "019cc781-6b9f-7362-ad33-8fc36f7661dd", card.Sessions[1].ID)
	assert.NotContains(t, card.Sessions[1].Label, "✓")

	assert.Equal(t, currentThreadID, card.CurrentID)
	require.Len(t, card.Buttons, 2)
}

func TestBuildSessionsCard_NoCurrentSession(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "test")

	sessions := c.Store.List(1, 10)
	// Don't set a current session.
	card := buildSessionsCard("", sessions)

	assert.Contains(t, card.Desc, "Current: none")
	assert.NotContains(t, card.Sessions[0].Label, "✓")
}

func TestBuildSessionsCard_LongTitle(t *testing.T) {
	c := newTestClient(t)
	longTitle := "This is a very long session title that exceeds the normal display limit and should be truncated"
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", longTitle)

	sessions := c.Store.List(1, 10)
	card := buildSessionsCard("", sessions)

	// Body should truncate to 36 runes.
	assert.Contains(t, card.Body, "…")
	// Dropdown label should truncate to 22 runes.
	for _, opt := range card.Sessions {
		labelRunes := []rune(opt.Label)
		// Each label is "N. shortID title..." — under 40 runes total.
		assert.LessOrEqual(t, len(labelRunes), 40)
	}
}

func TestBuildSessionsCard_UntitledSession(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "")

	sessions := c.Store.List(1, 10)
	card := buildSessionsCard("", sessions)

	assert.Contains(t, card.Body, "(untitled)")
	assert.Contains(t, card.Sessions[0].Label, "(untitled)")
}

// ── cleanSessionTitle tests ──

func TestCleanSessionTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello world", "hello world"},
		{"sender prefix", "[sender: 张三]\nhello world", "hello world"},
		{"sender prefix no newline", "[sender: Bob]hello", "hello"},
		{"sender only", "[sender: Alice]\n", ""},
		{"no sender prefix", "just a question", "just a question"},
		{"empty", "", ""},
		{"bracket but not sender", "[info] something", "[info] something"},
		{"sender with spaces", "[sender:  Bob Lee ]\nwhat is Go?", "what is Go?"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cleanSessionTitle(tt.input))
		})
	}
}

func TestBuildSessionsCard_CleansSenderPrefix(t *testing.T) {
	c := newTestClient(t)
	// Simulate what codexPrompt produces: "[sender: 张三]\n你好"
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "[sender: 张三]\n你好")

	sessions := c.Store.List(1, 10)
	card := buildSessionsCard("", sessions)

	// Body and dropdown should show "你好", not "[sender: 张三]"
	assert.NotContains(t, card.Body, "[sender:")
	assert.Contains(t, card.Body, "你好")
	assert.NotContains(t, card.Sessions[0].Label, "[sender:")
	assert.Contains(t, card.Sessions[0].Label, "你好")
}

func TestSessionListResponse_CleansSenderPrefix(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "[sender: Alice]\nWhat is Go?")

	msg := channel.Message{ChatID: 1, Text: "/sessions"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)

	assert.NotContains(t, resp.text, "[sender:")
	assert.Contains(t, resp.text, "What is Go?")
}

func TestIsCancel(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"/cancel", true},
		{"/stop", true},
		{" /cancel ", true},
		{"/cancel extra args", true},
		{"/stop now", true},
		{"/Cancel", false},       // case-sensitive
		{"cancel", false},        // no slash
		{"/new", false},          // different command
		{"/help", false},         // different command
		{"hello /cancel", false}, // not at start
		{"", false},              // empty
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			assert.Equal(t, tt.want, isCancel(tt.text))
		})
	}
}

func TestHandleCommand_CancelNotInCommandHandlers(t *testing.T) {
	// /cancel is handled before lockChat in processJob, not via handleCommand.
	// Verify handleCommand does NOT match /cancel so there's no conflict.
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/cancel"}
	_, ok := handleCommand(c, msg)
	assert.False(t, ok)
}

func TestHandleCommand_HelpIncludesCancel(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/help"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "/cancel")
}

// ── Additional coverage tests ──

func TestStripFormatRunes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello world", "hello world"},
		{"with zero-width space", "hello​world", "helloworld"},
		{"with soft hyphen", "hel­lo", "hello"},
		{"with BOM", "\xef\xbb\xbf" + "text", "text"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripFormatRunes(tt.input))
		})
	}
}

func TestNormalizeSessionID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple uuid", "019abc12-3456-7890-abcd-ef0123456789", "019abc12-3456-7890-abcd-ef0123456789"},
		{"with spaces", " 019abc12 ", "019abc12"},
		{"with zero-width chars", "019​abc", "019abc"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeSessionID(tt.input))
		})
	}
}

func TestTruncRunes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		n     int
		want  string
	}{
		{"short string", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"truncated", "hello world", 5, "hello…"},
		{"unicode", "你好世界测试", 3, "你好世…"},
		{"empty", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, truncRunes(tt.input, tt.n))
		})
	}
}

func TestCleanSessionTitle_GroupPrefix(t *testing.T) {
	input := "[shared group chat message]\n[sender: Bob]\nwhat is Go?"
	result := cleanSessionTitle(input)
	assert.NotContains(t, result, "[shared group chat message]")
}

func TestHandleCommand_Help_GroupFiltering(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/help", ChatType: "group"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	// In group mode, some commands should be filtered out.
	assert.Contains(t, resp.text, "/help")
}

func TestHandleCommand_Sessions_Empty_V2(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/sessions"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	// With no sessions, should still return a valid response.
	assert.NotEmpty(t, resp.text)
}

func TestHandleCommand_Sessions_WithSessions(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "019abc-session-1", "What is Go?")
	c.Store.Activate(1, "019abc-session-2", "Tell me about Rust")

	msg := channel.Message{ChatID: 1, Text: "/sessions"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "019abc")
}

func TestHandleCommand_Status(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/status"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "status")
}

func TestHandleCommand_Resume_NoArg(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/resume"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	// With no sessions, resume with no arg should show a no-sessions message
	assert.NotEmpty(t, resp.text)
}

func TestHandleCommand_Resume_WithArg(t *testing.T) {
	c := newTestClient(t)
	c.Store.Activate(1, "019abc-session-id", "initial prompt")

	msg := channel.Message{ChatID: 1, Text: "/resume 019abc-session-id"}
	resp, ok := handleCommand(c, msg)
	assert.True(t, ok)
	assert.Contains(t, resp.text, "019abc-session-id")
}

func TestHandleCommand_Unknown_V2(t *testing.T) {
	c := newTestClient(t)
	msg := channel.Message{ChatID: 1, Text: "/nonexistent"}
	_, ok := handleCommand(c, msg)
	assert.False(t, ok)
}

func TestStatusSoulState_Override(t *testing.T) {
	c := &codex.Client{
		SoulContent:   "global",
		SoulOverrides: map[string]string{"telegram": "override"},
	}
	assert.Equal(t, "loaded (channel override)", statusSoulState(c, "telegram"))
}

func TestStatusSoulState_Global(t *testing.T) {
	c := &codex.Client{SoulContent: "global content"}
	assert.Equal(t, "loaded (global)", statusSoulState(c, ""))
}

func TestStatusSoulState_GlobalPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SOUL.md")
	require.NoError(t, os.WriteFile(path, []byte("global content"), 0o644))

	c := &codex.Client{SoulPath: path}
	assert.Equal(t, "loaded (global)", statusSoulState(c, "feishu"))
}

func TestStatusSoulState_ChannelPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "SOUL-feishu.md")
	require.NoError(t, os.WriteFile(path, []byte("channel content"), 0o644))

	c := &codex.Client{
		SoulOverridePaths: map[string]string{"feishu": path},
	}
	assert.Equal(t, "loaded (channel override)", statusSoulState(c, "feishu"))
}

func TestStatusSoulState_NotConfigured(t *testing.T) {
	c := &codex.Client{}
	assert.Equal(t, "not configured", statusSoulState(c, ""))
}
