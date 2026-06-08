package gateway

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Rememorio/clawdex/internal/channel"
	"github.com/Rememorio/clawdex/internal/codex"
)

// --- mock driver ---

type mockDriver struct {
	name    string
	startFn func(ctx context.Context, h channel.Handler) error
}

func (m *mockDriver) Name() string { return m.name }
func (m *mockDriver) Start(ctx context.Context, h channel.Handler) error {
	return m.startFn(ctx, h)
}

// --- mock responder ---

type mockResponder struct {
	mu       sync.Mutex
	typings  []channel.Message
	replies  []replyRecord
	typingFn func(ctx context.Context, msg channel.Message) error
	replyFn  func(ctx context.Context, msg channel.Message, text string) error
}

type replyRecord struct {
	Msg  channel.Message
	Text string
}

func (m *mockResponder) Typing(ctx context.Context, msg channel.Message) error {
	m.mu.Lock()
	m.typings = append(m.typings, msg)
	m.mu.Unlock()
	if m.typingFn != nil {
		return m.typingFn(ctx, msg)
	}
	return nil
}

func (m *mockResponder) Reply(ctx context.Context, msg channel.Message, text string) error {
	m.mu.Lock()
	m.replies = append(m.replies, replyRecord{Msg: msg, Text: text})
	m.mu.Unlock()
	if m.replyFn != nil {
		return m.replyFn(ctx, msg, text)
	}
	return nil
}

func TestNew_MinWorkers(t *testing.T) {
	svc := New(&codex.Client{}, 0, "partial")
	assert.Equal(t, 1, svc.workers)
}

func TestNew_WorkerCount(t *testing.T) {
	svc := New(&codex.Client{}, 4, "partial")
	assert.Equal(t, 4, svc.workers)
}

func TestRun_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}

	svc := New(&codex.Client{Timeout: time.Second}, 1, "partial")

	errCh := make(chan error, 1)
	go func() { errCh <- svc.Run(ctx, driver) }()

	cancel()

	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRun_DriverError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	expectedErr := errors.New("driver boom")
	driver := &mockDriver{
		name: "failing",
		startFn: func(ctx context.Context, h channel.Handler) error {
			return expectedErr
		},
	}

	svc := New(&codex.Client{Timeout: time.Second}, 1, "partial")

	err := svc.Run(ctx, driver)
	assert.Equal(t, expectedErr, err)
}

func TestRun_NoDrivers(t *testing.T) {
	ctx := context.Background()
	svc := New(&codex.Client{Timeout: time.Second}, 1, "partial")

	err := svc.Run(ctx)
	assert.NoError(t, err)
}

func TestRun_MessageFlow(t *testing.T) {
	resp := &mockResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel:   "test",
				ChatID:    1,
				MessageID: 10,
				Text:      "hello",
			}, resp)
			// Give the worker time to process
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	// codex client will fail since there's no real binary,
	// but we just need to verify the flow: typing → run → reply
	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	require.Len(t, resp.typings, 1)
	assert.Equal(t, int64(1), resp.typings[0].ChatID)

	require.Len(t, resp.replies, 1)
	assert.Equal(t, int64(1), resp.replies[0].Msg.ChatID)
	// The reply text will be a codex error since no real binary exists, but
	// it should not be empty.
	assert.NotEmpty(t, resp.replies[0].Text)
}

func TestRun_NewCommandResetsSession(t *testing.T) {
	resp := &mockResponder{}

	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"sess-x"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	client := &codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
	}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			// 1. Send a normal message → session gets stored.
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 42, MessageID: 1, Text: "hello",
			}, resp)
			time.Sleep(300 * time.Millisecond)

			// 2. Send /new → session should be cleared.
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 42, MessageID: 2, Text: "/new",
			}, resp)
			time.Sleep(300 * time.Millisecond)

			return nil
		},
	}

	svc := New(client, 1, "partial")
	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	// Two replies: one from codex, one from /new.
	require.Len(t, resp.replies, 2)
	assert.Equal(t, "ok", resp.replies[0].Text)
	assert.Contains(t, resp.replies[1].Text, "Session cleared")

	// Only one typing indicator (for the normal message, not for /new).
	require.Len(t, resp.typings, 1)
}

func TestRun_NewCommandResetsSession_Standalone(t *testing.T) {
	resp := &mockResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "/new",
			}, resp)
			time.Sleep(300 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	require.Len(t, resp.replies, 1)
	assert.Contains(t, resp.replies[0].Text, "Session cleared")
	// No typing indicator for command messages.
	assert.Empty(t, resp.typings)
}

func TestRun_MultipleDrivers(t *testing.T) {
	var mu sync.Mutex
	started := 0

	makeDriver := func(name string) *mockDriver {
		return &mockDriver{
			name: name,
			startFn: func(ctx context.Context, h channel.Handler) error {
				mu.Lock()
				started++
				mu.Unlock()
				<-ctx.Done()
				return ctx.Err()
			},
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	svc := New(&codex.Client{Timeout: time.Second}, 1, "partial")

	errCh := make(chan error, 1)
	go func() { errCh <- svc.Run(ctx, makeDriver("a"), makeDriver("b")) }()

	// Wait for both drivers to start
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return started == 2
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	err := <-errCh
	assert.ErrorIs(t, err, context.Canceled)
}

// ── Feature 1: Keyboard dispatch ──

type mockKeyboardResponder struct {
	mockResponder
	mu           sync.Mutex
	keyboardCall *keyboardCallRecord
}

type keyboardCallRecord struct {
	Msg      channel.Message
	Text     string
	Keyboard [][]channel.KeyboardButton
}

func (m *mockKeyboardResponder) ReplyWithKeyboard(ctx context.Context, msg channel.Message, text string, keyboard [][]channel.KeyboardButton) error {
	m.mu.Lock()
	m.keyboardCall = &keyboardCallRecord{Msg: msg, Text: text, Keyboard: keyboard}
	m.mu.Unlock()
	return nil
}

func TestRun_HelpWithKeyboard(t *testing.T) {
	resp := &mockKeyboardResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "telegram", ChatID: 1, MessageID: 1, Text: "/help",
			}, resp)
			time.Sleep(300 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	require.NotNil(t, resp.keyboardCall)
	assert.Contains(t, resp.keyboardCall.Text, "Available commands")
	require.Len(t, resp.keyboardCall.Keyboard, 1)
	require.Len(t, resp.keyboardCall.Keyboard[0], 3)
	assert.Equal(t, "/new", resp.keyboardCall.Keyboard[0][0].CallbackData)
}

func TestRun_HelpFallbackPlainText(t *testing.T) {
	// When responder is NOT a KeyboardResponder, fallback to plain text.
	resp := &mockResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "/help",
			}, resp)
			time.Sleep(300 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	require.Len(t, resp.replies, 1)
	assert.Contains(t, resp.replies[0].Text, "Available commands")
}

func TestRun_WeComHelpWithKeyboard(t *testing.T) {
	resp := &mockKeyboardResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "wecom", ChatID: 1, MessageID: 1, Text: "/help",
			}, resp)
			time.Sleep(300 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	require.NotNil(t, resp.keyboardCall)
	assert.Contains(t, resp.keyboardCall.Text, "Available commands")
	require.Len(t, resp.keyboardCall.Keyboard, 1)
	require.Len(t, resp.keyboardCall.Keyboard[0], 3)
	assert.Equal(t, "/new", resp.keyboardCall.Keyboard[0][0].CallbackData)
}

// ── Feature 2: Streaming ──

type mockStreamResponder struct {
	mockResponder
	mu        sync.Mutex
	sendCalls []string
	editCalls []editRecord
	sentMsgID int64
}

type editRecord struct {
	ChatID    int64
	MessageID int64
	Text      string
}

func (m *mockStreamResponder) SendMessage(ctx context.Context, msg channel.Message, text string) (int64, error) {
	m.mu.Lock()
	m.sendCalls = append(m.sendCalls, text)
	m.sentMsgID++
	id := m.sentMsgID
	m.mu.Unlock()
	return id, nil
}

func (m *mockStreamResponder) EditMessage(ctx context.Context, chatID int64, messageID int64, text string) error {
	m.mu.Lock()
	m.editCalls = append(m.editCalls, editRecord{ChatID: chatID, MessageID: messageID, Text: text})
	m.mu.Unlock()
	return nil
}

func TestRun_StreamingFlow(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"sess-stream"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"streaming response"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockStreamResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "hello",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	// Should have sent at least one message via SendMessage.
	require.NotEmpty(t, resp.sendCalls)
	assert.Equal(t, "streaming response", resp.sendCalls[0])
}

func TestRun_NonStreamResponder_UsesRun(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"item.completed","item":{"type":"agent_message","text":"blocking response"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "hello",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	require.Len(t, resp.replies, 1)
	assert.Equal(t, "blocking response", resp.replies[0].Text)
}

func TestRun_GroupMessagesUseSeparateSessionsPerSender(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	logFile := filepath.Join(t.TempDir(), "mode.log")
	scriptContent := "#!/bin/sh\n" +
		"echo \"$1 $2\" >> " + logFile + "\n" +
		"if [ \"$2\" = \"resume\" ]; then\n" +
		"  echo '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"resumed\"}}'\n" +
		"  exit 0\n" +
		"fi\n" +
		"echo '{\"type\":\"thread.started\",\"thread_id\":\"group-sess\"}'\n" +
		"echo '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"fresh\"}}'\n"
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockResponder{}
	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel:   "telegram",
				ChatID:    1,
				SenderID:  101,
				ChatType:  "group",
				MessageID: 1,
				Text:      "first",
			}, resp)
			h.Handle(ctx, channel.Message{
				Channel:   "telegram",
				ChatID:    1,
				SenderID:  202,
				ChatType:  "group",
				MessageID: 2,
				Text:      "second",
			}, resp)
			time.Sleep(700 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}, 1, "off")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, "exec --json", lines[0])
	assert.Equal(t, "exec resume", lines[1])
}

func TestRun_GroupPromptIncludesSpeakerMetadata(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	promptFile := filepath.Join(t.TempDir(), "prompt.txt")
	scriptContent := "#!/bin/sh\n" +
		"for last; do\n" +
		"  :\n" +
		"done\n" +
		"printf '%s' \"$last\" > " + promptFile + "\n" +
		"echo '{\"type\":\"thread.started\",\"thread_id\":\"group-sess\"}'\n" +
		"echo '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"fresh\"}}'\n"
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockResponder{}
	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel:    "telegram",
				ChatID:     1,
				SenderID:   101,
				SenderName: "yuxanghuang",
				ChatType:   "group",
				MessageID:  1,
				Text:       "我说了什么？",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}, 1, "off")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	promptData, err := os.ReadFile(promptFile)
	require.NoError(t, err)
	prompt := string(promptData)
	assert.Contains(t, prompt, "[shared group chat message]")
	assert.Contains(t, prompt, "Speaker: yuxanghuang")
	assert.Contains(t, prompt, "SpeakerRef: u101")
	assert.Contains(t, prompt, "Message:\n我说了什么？")
	assert.Contains(t, prompt, "never attribute another")
}

func TestRun_SameChatJobsAreSerialized(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	stateDir := t.TempDir()
	lockFile := filepath.Join(stateDir, "active.lock")
	logFile := filepath.Join(stateDir, "concurrency.log")
	scriptContent := `#!/bin/sh
if [ -f "` + lockFile + `" ]; then
  echo concurrent >> ` + logFile + `
fi
touch ` + lockFile + `
sleep 1
rm -f ` + lockFile + `
echo '{"type":"thread.started","thread_id":"serialized"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockResponder{}
	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{Channel: "test", ChatID: 1, MessageID: 1, Text: "first"}, resp)
			h.Handle(ctx, channel.Message{Channel: "test", ChatID: 1, MessageID: 2, Text: "second"}, resp)
			time.Sleep(2500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}, 2, "off")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	data, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	assert.NotContains(t, string(data), "concurrent")
}

// ── Feature 4: Media detection ──

func TestExtractFilePaths_FindsExistingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "output.png")
	require.NoError(t, os.WriteFile(testFile, []byte("data"), 0o644))

	text := "I created the file at " + testFile + " for you."
	paths := extractFilePaths(text)
	require.Len(t, paths, 1)
	assert.Equal(t, testFile, paths[0])
}

func TestExtractFilePaths_FindsFileWithoutExtension(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "artifact")
	require.NoError(t, os.WriteFile(testFile, []byte("data"), 0o644))

	paths := extractFilePaths("Please send " + testFile)
	require.Len(t, paths, 1)
	assert.Equal(t, testFile, paths[0])
}

func TestExtractFilePaths_SkipsNonExistent(t *testing.T) {
	text := "I created the file at /tmp/nonexistent-abcdef123.png for you."
	paths := extractFilePaths(text)
	assert.Empty(t, paths)
}

func TestExtractFilePaths_Deduplicates(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "output.jpg")
	require.NoError(t, os.WriteFile(testFile, []byte("data"), 0o644))

	text := "File at " + testFile + " and again " + testFile
	paths := extractFilePaths(text)
	assert.Len(t, paths, 1)
}

func TestExtractFilePaths_ExpandsHomeShortcut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	testFile := filepath.Join(home, ".clawdex", "SOUL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(testFile), 0o755))
	require.NoError(t, os.WriteFile(testFile, []byte("data"), 0o644))

	paths := extractFilePaths("Please send ~/.clawdex/SOUL.md")
	require.Len(t, paths, 1)
	assert.Equal(t, testFile, paths[0])
}

func TestExtractFilePaths_DeduplicatesCopiedFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	homeFile := filepath.Join(home, ".clawdex", "SOUL.md")
	workspaceFile := filepath.Join(t.TempDir(), "SOUL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(homeFile), 0o755))
	require.NoError(t, os.WriteFile(homeFile, []byte("same"), 0o644))
	require.NoError(t, os.WriteFile(workspaceFile, []byte("same"), 0o644))

	text := "Try [SOUL.md](sandbox:" + workspaceFile + ") and ~/.clawdex/SOUL.md"
	paths := extractFilePaths(text)
	require.Len(t, paths, 1)
	assert.Equal(t, workspaceFile, paths[0])
}

func TestExtractFilePaths_DeduplicatesSameContentDifferentNames(t *testing.T) {
	tmpDir := t.TempDir()
	firstFile := filepath.Join(tmpDir, "Eyjafjalla_operator_report.wav")
	secondFile := filepath.Join(tmpDir, "干员报到.wav")
	require.NoError(t, os.WriteFile(firstFile, []byte("same audio"), 0o644))
	require.NoError(t, os.WriteFile(secondFile, []byte("same audio"), 0o644))

	text := "Send " + firstFile + " and " + secondFile
	paths := extractFilePaths(text)
	require.Len(t, paths, 1)
	assert.Equal(t, firstFile, paths[0])
}

func TestExtractFilePaths_TrimsTrailingPunctuation(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "report.custom")
	require.NoError(t, os.WriteFile(testFile, []byte("data"), 0o644))

	paths := extractFilePaths("Done: " + testFile + "。")
	require.Len(t, paths, 1)
	assert.Equal(t, testFile, paths[0])
}

func TestExtractFilePaths_IgnoresPathsInInlineCode(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "runner.cover")
	require.NoError(t, os.WriteFile(testFile, []byte("mode: atomic\n"), 0o644))

	// Path inside inline backticks should be ignored.
	text := "本地验证：\n- `go test ./runner -coverprofile=" + testFile + " -count=1` 通过"
	paths := extractFilePaths(text)
	assert.Empty(t, paths)
}

func TestExtractFilePaths_IgnoresPathsInFencedCodeBlock(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "output.png")
	require.NoError(t, os.WriteFile(testFile, []byte("data"), 0o644))

	text := "Results:\n```\ngenerated " + testFile + "\n```\nDone."
	paths := extractFilePaths(text)
	assert.Empty(t, paths)
}

func TestExtractFilePaths_DetectsPathOutsideCodeSpan(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "report.png")
	require.NoError(t, os.WriteFile(testFile, []byte("data"), 0o644))

	// Path outside backticks should still be detected.
	text := "Here is the report: " + testFile + " and `some code`"
	paths := extractFilePaths(text)
	require.Len(t, paths, 1)
	assert.Equal(t, testFile, paths[0])
}

type mockMediaResponder struct {
	mockResponder
	mu                    sync.Mutex
	mediaCall             *mediaCallRecord
	suppressTextWithMedia bool
}

type mediaCallRecord struct {
	Msg       channel.Message
	Caption   string
	FilePaths []string
}

func (m *mockMediaResponder) ReplyWithMedia(ctx context.Context, msg channel.Message, caption string, filePaths []string) error {
	m.mu.Lock()
	m.mediaCall = &mediaCallRecord{Msg: msg, Caption: caption, FilePaths: filePaths}
	m.mu.Unlock()
	return nil
}

func (m *mockMediaResponder) SuppressTextWithMedia() bool {
	return m.suppressTextWithMedia
}

type mockStreamingMediaResponder struct {
	mockStreamResponder
	mediaCall             *mediaCallRecord
	suppressTextWithMedia bool
}

func (m *mockStreamingMediaResponder) ReplyWithMedia(ctx context.Context, msg channel.Message, caption string, filePaths []string) error {
	m.mu.Lock()
	m.mediaCall = &mediaCallRecord{Msg: msg, Caption: caption, FilePaths: filePaths}
	m.mu.Unlock()
	return nil
}

func (m *mockStreamingMediaResponder) SuppressTextWithMedia() bool {
	return m.suppressTextWithMedia
}

func TestRun_MediaFlow(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "output.png")
	require.NoError(t, os.WriteFile(testFile, []byte("image data"), 0o644))

	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := "#!/bin/sh\n" +
		"echo '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Here is the file: " + testFile + "\"}}'\n"
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockMediaResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "create an image",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	require.NotNil(t, resp.mediaCall)
	require.Len(t, resp.mediaCall.FilePaths, 1)
	assert.Equal(t, testFile, resp.mediaCall.FilePaths[0])
}

func TestReplyWithMediaDetection_SuppressesCaptionWhenRequested(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "voice.wav")
	require.NoError(t, os.WriteFile(testFile, []byte("voice data"), 0o644))

	resp := &mockMediaResponder{suppressTextWithMedia: true}
	svc := New(&codex.Client{}, 1, "partial")
	svc.replyWithMediaDetection(
		context.Background(),
		job{
			msg:       channel.Message{Channel: "test", ChatID: 1},
			responder: resp,
		},
		"Here is the file: "+testFile,
	)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	require.NotNil(t, resp.mediaCall)
	assert.Empty(t, resp.mediaCall.Caption)
	require.Len(t, resp.mediaCall.FilePaths, 1)
	assert.Equal(t, testFile, resp.mediaCall.FilePaths[0])
}

func TestRun_StreamingMediaSuppressionSkipsTextSend(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "voice.wav")
	require.NoError(t, os.WriteFile(testFile, []byte("voice data"), 0o644))

	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := "#!/bin/sh\n" +
		"echo '{\"type\":\"thread.started\",\"thread_id\":\"sess-media\"}'\n" +
		"echo '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Here is the file: " + testFile + "\"}}'\n"
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockStreamingMediaResponder{suppressTextWithMedia: true}
	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "send voice",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	assert.Empty(t, resp.sendCalls)
	require.NotNil(t, resp.mediaCall)
	assert.Empty(t, resp.mediaCall.Caption)
	require.Len(t, resp.mediaCall.FilePaths, 1)
	assert.Equal(t, testFile, resp.mediaCall.FilePaths[0])
}

// ── Feature: Status Reactions ──

type mockStatusReactor struct {
	mockStreamResponder
	mu        sync.Mutex
	reactions []reactionRecord
}

type reactionRecord struct {
	ChatID    int64
	MessageID int64
	Emoji     string
}

func (m *mockStatusReactor) SetReaction(ctx context.Context, chatID, messageID int64, emoji string) error {
	m.mu.Lock()
	m.reactions = append(m.reactions, reactionRecord{ChatID: chatID, MessageID: messageID, Emoji: emoji})
	m.mu.Unlock()
	return nil
}

func TestProcessJob_StatusReactions(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"item.completed","item":{"type":"agent_message","text":"done"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockStatusReactor{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 10, Text: "hello",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	// Should have at least 2 reactions: 👀 then 👍
	require.GreaterOrEqual(t, len(resp.reactions), 2)
	assert.Equal(t, "👀", resp.reactions[0].Emoji)
	assert.Equal(t, "👍", resp.reactions[len(resp.reactions)-1].Emoji)
}

// ── Feature: Draft Streaming ──

type mockDraftResponder struct {
	mockStreamResponder
	mu            sync.Mutex
	draftCalls    []draftRecord
	finalizeCalls []finalizeRecord
}

type draftRecord struct {
	Msg  channel.Message
	Text string
}

type finalizeRecord struct {
	ChatID    int64
	MessageID int64
	Text      string
}

func (m *mockDraftResponder) SendDraft(ctx context.Context, msg channel.Message, text string) (int64, error) {
	m.mu.Lock()
	m.draftCalls = append(m.draftCalls, draftRecord{Msg: msg, Text: text})
	id := int64(len(m.draftCalls)) + 100
	m.mu.Unlock()
	return id, nil
}

func (m *mockDraftResponder) FinalizeDraft(ctx context.Context, chatID int64, messageID int64, text string) error {
	m.mu.Lock()
	m.finalizeCalls = append(m.finalizeCalls, finalizeRecord{ChatID: chatID, MessageID: messageID, Text: text})
	m.mu.Unlock()
	return nil
}

func TestRun_DraftStreamingFlow(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"sess-draft"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"draft response"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockDraftResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "hello",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	// Should have sent at least one draft (initial message via SendDraft).
	require.NotEmpty(t, resp.draftCalls)
	assert.Equal(t, int64(1), resp.draftCalls[0].Msg.ChatID)
	assert.Equal(t, "draft response", resp.draftCalls[0].Text)
}

// ── Feature: Stream Guard (WeCom 6-minute timeout) ──

type mockStreamFinisherResponder struct {
	mockStreamResponder
	mu            sync.Mutex
	thinkingCalls []channel.Message
	finishCalls   []finishRecord
}

type finishRecord struct {
	ChatID int64
	Text   string
}

func (m *mockStreamFinisherResponder) SendThinking(ctx context.Context, msg channel.Message) error {
	m.mu.Lock()
	m.thinkingCalls = append(m.thinkingCalls, msg)
	m.mu.Unlock()
	return nil
}

func (m *mockStreamFinisherResponder) FinishStream(ctx context.Context, chatID int64, text string) error {
	m.mu.Lock()
	m.finishCalls = append(m.finishCalls, finishRecord{ChatID: chatID, Text: text})
	m.mu.Unlock()
	return nil
}

func TestRunEditStreaming_StreamGuardClosesOnTimeout(t *testing.T) {
	origMaxAge := streamMaxAge
	streamMaxAge = 200 * time.Millisecond
	t.Cleanup(func() { streamMaxAge = origMaxAge })

	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// Script sleeps longer than streamMaxAge, then emits the final result.
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"sess-guard"}'
echo '{"type":"item.streaming","item":{"type":"agent_message","text":"partial"}}'
sleep 1
echo '{"type":"item.completed","item":{"type":"agent_message","text":"final complete result"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockStreamFinisherResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "wecom", ChatID: 42, MessageID: 1, Text: "review this PR",
			}, resp)
			time.Sleep(3 * time.Second)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	// FinishStream should have been called by the guard with the "still working" hint.
	require.NotEmpty(t, resp.finishCalls, "FinishStream should have been called by guard timer")
	assert.Contains(t, resp.finishCalls[0].Text, finishTextStillWorking)
	assert.Equal(t, int64(42), resp.finishCalls[0].ChatID)

	// After codex completes, a Reply should deliver the final result.
	resp.mockStreamResponder.mu.Lock()
	defer resp.mockStreamResponder.mu.Unlock()
	require.NotEmpty(t, resp.mockStreamResponder.mockResponder.replies, "Reply should deliver the final result after stream expired")
	assert.Equal(t, "final complete result", resp.mockStreamResponder.mockResponder.replies[0].Text)
}

// TestRunEditStreaming_StreamGuardCompletedPrefersFinalAgentMessage verifies
// that an expired stream does not replay intermediary status messages when
// Codex later completes normally. The final new message should match the
// last completed agent_message, just like the normal non-expired final edit.
func TestRunEditStreaming_StreamGuardCompletedPrefersFinalAgentMessage(t *testing.T) {
	origMaxAge := streamMaxAge
	streamMaxAge = 200 * time.Millisecond
	t.Cleanup(func() { streamMaxAge = origMaxAge })

	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// Codex emits an early status message, then finishes after the guard has
	// closed the WeCom stream. The post-expired fallback should send only the
	// final answer, not the streaming accumulator.
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"sess-replay"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"status: checking files"}}'
sleep 1
echo '{"type":"item.completed","item":{"type":"agent_message","text":"final complete result"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockStreamFinisherResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "wecom", ChatID: 99, MessageID: 1, Text: "long task",
			}, resp)
			time.Sleep(3 * time.Second)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mockStreamResponder.mu.Lock()
	defer resp.mockStreamResponder.mu.Unlock()
	require.NotEmpty(t, resp.mockStreamResponder.mockResponder.replies,
		"Reply should deliver the final result after stream expired")
	got := resp.mockStreamResponder.mockResponder.replies[0].Text
	assert.Equal(t, "final complete result", got)
}

// TestRunEditStreaming_StreamGuardReplayPrependsTimeoutNotice verifies that
// when codex itself hits its CommandTimeout (Codex client's WithTimeout
// fires DeadlineExceeded) after the wecom guard already closed the stream,
// the replayed message is prefixed with a clear timeout notice so the user
// understands why the conversation was cut off mid-action.
func TestRunEditStreaming_StreamGuardReplayPrependsTimeoutNotice(t *testing.T) {
	origMaxAge := streamMaxAge
	streamMaxAge = 200 * time.Millisecond
	t.Cleanup(func() { streamMaxAge = origMaxAge })

	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// Emit one agent_message, then sleep past the codex client timeout.
	// On SIGINT (sent via setGracefulCancel) we exit cleanly; the parent
	// will see ctx.Err() == context.DeadlineExceeded.
	scriptContent := `#!/bin/sh
trap 'exit 0' INT
echo '{"type":"thread.started","thread_id":"sess-timeout"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"partial work"}}'
sleep 30
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockStreamFinisherResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "wecom", ChatID: 5, MessageID: 1, Text: "do work",
			}, resp)
			time.Sleep(2 * time.Second)
			return nil
		},
	}

	// codex Timeout < the script's sleep, so RunStream's WithTimeout fires
	// DeadlineExceeded — the exact path that produced the silent
	// "fragment-only" replay reported by users.
	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 800 * time.Millisecond,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mockStreamResponder.mu.Lock()
	defer resp.mockStreamResponder.mu.Unlock()
	require.NotEmpty(t, resp.mockStreamResponder.mockResponder.replies,
		"Reply should deliver the partial result after timeout")
	got := resp.mockStreamResponder.mockResponder.replies[0].Text
	assert.Contains(t, got, "Codex hit timeout",
		"replay should prefix a timeout notice when ctx.Err() == DeadlineExceeded")
	assert.Contains(t, got, "partial work",
		"replay should still surface what codex produced before the timeout")
}

func TestRunEditStreaming_NormalFlowNoGuardFired(t *testing.T) {
	origMaxAge := streamMaxAge
	streamMaxAge = 10 * time.Second // Large enough to never fire.
	t.Cleanup(func() { streamMaxAge = origMaxAge })

	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"sess-noguard"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"quick answer"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockStreamFinisherResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "wecom", ChatID: 7, MessageID: 1, Text: "hello",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	// FinishStream should have been called exactly once — by the normal flow, not the guard.
	require.Len(t, resp.finishCalls, 1, "FinishStream should be called once in normal flow")
	assert.NotContains(t, resp.finishCalls[0].Text, finishTextStillWorking,
		"normal finish should NOT contain the still-working hint")
	assert.Equal(t, int64(7), resp.finishCalls[0].ChatID)

	// No fallback Reply should be needed — the stream edit path handles everything.
	resp.mockStreamResponder.mu.Lock()
	defer resp.mockStreamResponder.mu.Unlock()
	assert.Empty(t, resp.mockStreamResponder.mockResponder.replies,
		"no Reply fallback expected for quick tasks")
}

func TestProcessJob_StreamingOff(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"sess-off"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"non-streaming"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	// Use a StreamResponder, but streaming=off should bypass it.
	resp := &mockStreamResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "hello",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
	}, 1, "off")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	// With streaming off, should use Reply (from the base mockResponder), not SendMessage.
	assert.Empty(t, resp.sendCalls, "streaming=off should not use SendMessage")
	require.NotEmpty(t, resp.mockResponder.replies, "streaming=off should use Reply")
	assert.Equal(t, "non-streaming", resp.mockResponder.replies[0].Text)
}

func TestBuildWelcomeCard(t *testing.T) {
	svc := New(&codex.Client{}, 1, "partial")
	card := svc.BuildWelcomeCard(context.Background(), channel.Message{ChatID: 1})
	require.NotNil(t, card)
	assert.Contains(t, card.Title, "Welcome")
	assert.NotEmpty(t, card.Desc)
	assert.Contains(t, card.Body, "/help")
	assert.Contains(t, card.Body, "/sessions")
	require.Len(t, card.Buttons, 2)
	assert.Equal(t, "/help", card.Buttons[0].Text)
	assert.Equal(t, "/sessions:help", card.Buttons[0].CallbackData)
	assert.Equal(t, "/sessions", card.Buttons[1].Text)
	assert.Equal(t, "/sessions:sessions", card.Buttons[1].CallbackData)
}

// ── Feature: Native Thinking Display (WeCom) ──

// TestStripThinkingTags_PreservesNonThinkingContent verifies that text without
// thinking tags is returned as-is.
func TestStripThinkingTags_PreservesNonThinkingContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain text", "hello world", "hello world"},
		{"empty", "", ""},
		{"code block", "```go\nfmt.Println()\n```", "```go\nfmt.Println()\n```"},
		{"html-like but not think", "<div>content</div>", "<div>content</div>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stripThinkingTags(tt.input))
		})
	}
}

// TestStripThinkingTags_RemovesThinkingContent verifies that <think> blocks
// are stripped and only visible content remains.
func TestStripThinkingTags_RemovesThinkingContent(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"basic think block",
			"<think>reasoning about the problem</think>\nfinal answer",
			"final answer",
		},
		{
			"think at end",
			"visible text\n<think>hidden reasoning</think>",
			"visible text",
		},
		{
			"multiple think blocks",
			"<think>step 1</think>\npart 1\n<think>step 2</think>\npart 2",
			"part 1\n\npart 2",
		},
		{
			"thought tag variant",
			"<thought>reasoning</thought>\nresult",
			"result",
		},
		{
			"antthinking variant",
			"<antThinking>internal</antThinking>\nvisible",
			"visible",
		},
		{
			"unclosed think tag (streaming)",
			"<think>still thinking...",
			"",
		},
		{
			"think inside code block preserved",
			"```\n<think>not stripped</think>\n```",
			"```\n<think>not stripped</think>\n```",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripThinkingTags(tt.input)
			assert.Equal(t, tt.want, result)
		})
	}
}

// TestStripThinkingTags_WeComStreamingBehavior verifies the contract for
// WeCom native thinking display:
// - When ThinkingIndicator is active, stream frames KEEP <think> tags
// - The final finish frame STRIPS <think> tags
// This test validates the stripThinkingTags function used in the finish path.
func TestStripThinkingTags_WeComStreamingBehavior(t *testing.T) {
	// Simulates what the stream callback does:
	// thinkingSent=true → fullText keeps tags (for stream frames)
	// Final output → stripThinkingTags removes them (for finish frame)
	streamText := "<think>reasoning about the problem</think>\nfinal answer"

	// Stream frame: preserve tags for WeCom rendering
	assert.Contains(t, streamText, "<think>")
	assert.Contains(t, streamText, "reasoning about the problem")

	// Finish frame: strip tags for clean display
	finishText := stripThinkingTags(streamText)
	assert.NotContains(t, finishText, "<think>")
	assert.NotContains(t, finishText, "reasoning about the problem")
	assert.Equal(t, "final answer", finishText)
}

// TestStripThinkingTags_WithoutIndicator verifies that when ThinkingIndicator
// is NOT active, thinking tags are stripped from ALL frames.
func TestStripThinkingTags_WithoutIndicator(t *testing.T) {
	input := "<think>hidden reasoning</think>\nvisible reply"
	result := stripThinkingTags(input)
	assert.NotContains(t, result, "<think>")
	assert.NotContains(t, result, "hidden reasoning")
	assert.Contains(t, result, "visible reply")
}

// TestStripThinkingTags_MultiChunk verifies multi-chunk thinking handling.
func TestStripThinkingTags_MultiChunk(t *testing.T) {
	// First chunk
	chunk1 := "<think>step 1 reasoning</think>\nfirst part"
	stripped1 := stripThinkingTags(chunk1)
	assert.Equal(t, "first part", stripped1)

	// Second chunk (accumulated)
	chunk2 := "<think>step 2 reasoning</think>\nsecond part"
	stripped2 := stripThinkingTags(chunk2)
	assert.Equal(t, "second part", stripped2)
	assert.NotContains(t, stripped2, "<think>")
}

func TestRunEditStreaming_ThinkingSentNoChunks_NoDuplicateReply(t *testing.T) {
	// When thinking is sent but codex produces no stream chunks (e.g. empty
	// agent_message), FinishStream should deliver the final text and no
	// separate Reply should be sent (avoiding duplicate messages).
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// Codex returns valid JSONL but agent_message text is empty.
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"sess-empty"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":""}}'
echo '{"type":"turn.completed","usage":{"input_tokens":100}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockStreamFinisherResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "draw something",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir:    t.TempDir(),
		Timeout:    5 * time.Second,
		Executable: script,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	// FinishStream should have been called with the fallback text.
	resp.mu.Lock()
	finishCalls := resp.finishCalls
	resp.mu.Unlock()
	require.NotEmpty(t, finishCalls, "FinishStream should have been called")

	// The Reply method on the base responder should NOT have been called
	// since FinishStream already delivered the content.
	resp.mockStreamResponder.mu.Lock()
	replies := resp.mockStreamResponder.mockResponder.replies
	resp.mockStreamResponder.mu.Unlock()
	assert.Empty(t, replies,
		"Reply should not be called when FinishStream succeeded (would cause duplicate)")
}

// ── Feature: /cancel ──

func TestCancel_NoRunningJob(t *testing.T) {
	resp := &mockResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "/cancel",
			}, resp)
			time.Sleep(200 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
	}, 2, "off")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	require.Len(t, resp.replies, 1)
	assert.Equal(t, "No running task to cancel.", resp.replies[0].Text)
	// No typing indicator for /cancel.
	assert.Empty(t, resp.typings)
}

func TestCancel_CancelsRunningJob(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// Codex sleeps for a long time; cancel should interrupt it.
	scriptContent := `#!/bin/sh
trap 'echo "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"interrupted\"}}"; exit 0' INT
echo '{"type":"thread.started","thread_id":"sess-cancel"}'
sleep 30
echo '{"type":"item.completed","item":{"type":"agent_message","text":"should not reach"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			// Send a normal message that starts a long-running codex job.
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "do work",
			}, resp)
			// Wait a bit for the job to start.
			time.Sleep(500 * time.Millisecond)
			// Send /cancel to abort it.
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 2, Text: "/cancel",
			}, resp)
			// Wait for everything to settle.
			time.Sleep(1500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 30 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}, 2, "off")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	// We expect at most one /cancel confirmation reply ("❌ Cancelled.").
	// The cancelled codex's partial output ("interrupted") must NOT be
	// pushed as a final reply on top of the confirmation; otherwise the
	// user sees a duplicate-message bug (regression test for the case
	// reported in https://github.com/Rememorio/clawdex — "/cancel followed
	// by codex's partial agent_message rendered as final reply").
	var cancelReply bool
	for _, r := range resp.replies {
		if r.Text == finishTextCancelled {
			cancelReply = true
		}
	}
	assert.True(t, cancelReply, "expected cancel confirmation reply, got: %v", resp.replies)
	// In the streaming-off configuration used by this test the responder
	// has no StreamFinisher, so /cancel surfaces via Reply rather than via
	// FinishStream. The point is that we should NOT see "interrupted" come
	// through as a separate reply.
	for _, r := range resp.replies {
		assert.NotEqual(t, "interrupted", r.Text,
			"codex partial output must not be pushed as final after /cancel")
	}
}

func TestCancel_CancelsRunningJob_StatusReaction(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
trap 'echo "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"interrupted\"}}"; exit 0' INT
echo '{"type":"thread.started","thread_id":"sess-cancel-status"}'
sleep 30
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockStatusReactor{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "do work",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 2, Text: "/cancel",
			}, resp)
			time.Sleep(1500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 30 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}, 2, "off")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	reactions := append([]reactionRecord(nil), resp.reactions...)
	resp.mu.Unlock()

	require.GreaterOrEqual(t, len(reactions), 2)
	assert.Equal(t, "👀", reactions[0].Emoji)
	assert.Equal(t, "❌", reactions[len(reactions)-1].Emoji)
	for _, r := range reactions {
		assert.NotEqual(t, "👍", r.Emoji)
	}
}

func TestCancel_StopAlias(t *testing.T) {
	resp := &mockResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "/stop",
			}, resp)
			time.Sleep(200 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
	}, 2, "off")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	require.Len(t, resp.replies, 1)
	// /stop with no running job returns the same "no task" message.
	assert.Equal(t, "No running task to cancel.", resp.replies[0].Text)
}

// TestCancel_DuringStream_FinishStreamPath verifies the duplicate-message bug
// fix: when /cancel hits a job whose responder supports FinishStream (e.g.
// WeCom), the partial agent_message that codex emits during the SIGINT grace
// period must NOT be pushed as a final reply on top of /cancel's own
// confirmation. Instead, the streaming goroutine should close the stream
// frame with "❌ Cancelled." and /cancel should stay silent.
func TestCancel_DuringStream_FinishStreamPath(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// Codex starts streaming, then on SIGINT emits a partial agent_message
	// inside the 5s grace period — exactly the shape that produced the
	// reported bug.
	scriptContent := `#!/bin/sh
trap 'echo "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"partial during cancel\"}}"; exit 0' INT
echo '{"type":"thread.started","thread_id":"sess-cancel-stream"}'
echo '{"type":"item.streaming","item":{"type":"agent_message","text":"working..."}}'
sleep 30
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockStreamFinisherResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "wecom", ChatID: 7, MessageID: 1, Text: "do work",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			h.Handle(ctx, channel.Message{
				Channel: "wecom", ChatID: 7, MessageID: 2, Text: "/cancel",
			}, resp)
			time.Sleep(2 * time.Second)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 30 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}, 2, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	finishCalls := append([]finishRecord(nil), resp.finishCalls...)
	resp.mu.Unlock()

	resp.mockStreamResponder.mu.Lock()
	replies := append([]replyRecord(nil), resp.mockStreamResponder.mockResponder.replies...)
	resp.mockStreamResponder.mu.Unlock()

	// Exactly one FinishStream — the cancel notice. No "still working" hint
	// (the guard timer is far away), no Done text (the job didn't complete),
	// no media-sent text.
	require.Len(t, finishCalls, 1, "expected exactly one FinishStream on cancel, got: %v", finishCalls)
	assert.Equal(t, finishTextCancelled, finishCalls[0].Text,
		"FinishStream should carry the cancel notice")

	// /cancel must NOT have produced its own Reply — selfAcksCancel told it
	// to stay silent. And codex's partial agent_message must NOT have been
	// pushed as a final reply (the actual reported bug).
	for _, r := range replies {
		assert.NotEqual(t, finishTextCancelled, r.Text,
			"cancel notice should arrive via FinishStream, not Reply (would duplicate)")
		assert.NotContains(t, r.Text, "partial during cancel",
			"codex partial agent_message must not be pushed as final after /cancel")
	}
}

// ── splitByRuneLimit tests ──

func TestSplitByRuneLimit_Empty(t *testing.T) {
	parts := splitByRuneLimit("", 100)
	assert.Equal(t, []string{"(empty response)"}, parts)
}

func TestSplitByRuneLimit_WhitespaceOnly(t *testing.T) {
	parts := splitByRuneLimit("   \n\t  ", 100)
	assert.Equal(t, []string{"(empty response)"}, parts)
}

func TestSplitByRuneLimit_Short(t *testing.T) {
	parts := splitByRuneLimit("hello world", 100)
	assert.Equal(t, []string{"hello world"}, parts)
}

func TestSplitByRuneLimit_ExactLimit(t *testing.T) {
	parts := splitByRuneLimit("abcde", 5)
	assert.Equal(t, []string{"abcde"}, parts)
}

func TestSplitByRuneLimit_SplitsCorrectly(t *testing.T) {
	parts := splitByRuneLimit("abcdefghij", 3)
	assert.Equal(t, []string{"abc", "def", "ghi", "j"}, parts)
}

func TestSplitByRuneLimit_Unicode(t *testing.T) {
	parts := splitByRuneLimit("你好世界测试六字", 4)
	assert.Equal(t, []string{"你好世界", "测试六字"}, parts)
}

func TestSplitByRuneLimit_WithLeadingTrailingWhitespace(t *testing.T) {
	parts := splitByRuneLimit("  hello  ", 100)
	assert.Equal(t, []string{"hello"}, parts)
}

// ── resolveFinishText tests ──

func TestResolveFinishText_WithText(t *testing.T) {
	svc := New(&codex.Client{}, 1, "partial")
	result := svc.resolveFinishText("some output text", nil)
	assert.Equal(t, "some output text", result)
}

func TestResolveFinishText_WithMedia(t *testing.T) {
	svc := New(&codex.Client{}, 1, "partial")
	result := svc.resolveFinishText("", []string{"/tmp/file.png"})
	assert.Equal(t, finishTextMediaSent, result)
}

func TestResolveFinishText_Empty(t *testing.T) {
	svc := New(&codex.Client{}, 1, "partial")
	result := svc.resolveFinishText("", nil)
	assert.Equal(t, finishTextDone, result)
}

func TestResolveFinishText_WhitespaceOnly(t *testing.T) {
	svc := New(&codex.Client{}, 1, "partial")
	result := svc.resolveFinishText("   \n\t  ", nil)
	assert.Equal(t, finishTextDone, result)
}

func TestResolveFinishText_TextWithMedia(t *testing.T) {
	svc := New(&codex.Client{}, 1, "partial")
	result := svc.resolveFinishText("here is your file", []string{"/tmp/f.png"})
	assert.Equal(t, "here is your file", result)
}

// ── extractFilePaths additional tests ──

func TestExtractFilePaths_SkipsDirectories(t *testing.T) {
	dir := t.TempDir()
	// The path is a directory, not a file.
	paths := extractFilePaths("Check " + dir)
	assert.Empty(t, paths)
}

func TestExtractFilePaths_MultipleExistingFiles(t *testing.T) {
	tmpDir := t.TempDir()
	f1 := filepath.Join(tmpDir, "a.png")
	f2 := filepath.Join(tmpDir, "b.jpg")
	require.NoError(t, os.WriteFile(f1, []byte("data1"), 0o644))
	require.NoError(t, os.WriteFile(f2, []byte("data2"), 0o644))

	text := "File 1: " + f1 + " and file 2: " + f2
	paths := extractFilePaths(text)
	assert.Len(t, paths, 2)
	assert.Contains(t, paths, f1)
	assert.Contains(t, paths, f2)
}

func TestExtractFilePaths_HomePathNonExistent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths := extractFilePaths("Check ~/nonexistent-file-xyzzy")
	assert.Empty(t, paths)
}

// ── cleanupMediaDirs tests ──

func TestCleanupMediaDirs_RemovesMatchingDirs(t *testing.T) {
	// Create a temp dir that matches the pattern.
	tmpDir, err := os.MkdirTemp("", "clawdex-tg-media-")
	require.NoError(t, err)

	f := filepath.Join(tmpDir, "photo.jpg")
	require.NoError(t, os.WriteFile(f, []byte("img"), 0o644))

	// Verify it exists.
	_, err = os.Stat(tmpDir)
	require.NoError(t, err)

	cleanupMediaDirs([]string{f})

	// Should be removed.
	_, err = os.Stat(tmpDir)
	assert.True(t, os.IsNotExist(err))
}

func TestCleanupMediaDirs_IgnoresNonMatchingDirs(t *testing.T) {
	tmpDir := t.TempDir()
	f := filepath.Join(tmpDir, "file.txt")
	require.NoError(t, os.WriteFile(f, []byte("data"), 0o644))

	cleanupMediaDirs([]string{f})

	// Regular dir should NOT be removed.
	_, err := os.Stat(tmpDir)
	assert.NoError(t, err)
}

func TestCleanupMediaDirs_EmptyPaths(t *testing.T) {
	// Should not panic with nil or empty paths.
	cleanupMediaDirs(nil)
	cleanupMediaDirs([]string{})
}

func TestCleanupMediaDirs_WeComMediaDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "clawdex-wecom-media-")
	require.NoError(t, err)

	f := filepath.Join(tmpDir, "audio.wav")
	require.NoError(t, os.WriteFile(f, []byte("wav"), 0o644))

	cleanupMediaDirs([]string{f})

	_, err = os.Stat(tmpDir)
	assert.True(t, os.IsNotExist(err))
}

func TestCleanupMediaDirs_MultipleSets(t *testing.T) {
	dir1, err := os.MkdirTemp("", "clawdex-tg-media-")
	require.NoError(t, err)
	f1 := filepath.Join(dir1, "a.png")
	require.NoError(t, os.WriteFile(f1, []byte("a"), 0o644))

	dir2, err := os.MkdirTemp("", "clawdex-wecom-media-")
	require.NoError(t, err)
	f2 := filepath.Join(dir2, "b.pdf")
	require.NoError(t, os.WriteFile(f2, []byte("b"), 0o644))

	cleanupMediaDirs([]string{f1}, []string{f2})

	_, err = os.Stat(dir1)
	assert.True(t, os.IsNotExist(err))
	_, err = os.Stat(dir2)
	assert.True(t, os.IsNotExist(err))
}

// ── codexPrompt tests (via scope_test.go patterns) ──

func TestCodexPrompt_GroupWithoutSenderName(t *testing.T) {
	msg := channel.Message{
		Channel:  "telegram",
		ChatID:   -100,
		SenderID: 42,
		ChatType: "group",
		Text:     "hello",
	}
	prompt := codexPrompt(msg)
	assert.Contains(t, prompt, "[shared group chat message]")
	assert.Contains(t, prompt, "Speaker: user-42")
	assert.Contains(t, prompt, "SpeakerRef: u42")
	assert.Contains(t, prompt, "Message:\nhello")
}

func TestCodexPrompt_GroupWithThread(t *testing.T) {
	msg := channel.Message{
		Channel:    "telegram",
		ChatID:     -100,
		SenderID:   7,
		SenderName: "Alice",
		ChatType:   "group",
		ThreadID:   99,
		Text:       "in thread",
	}
	prompt := codexPrompt(msg)
	assert.Contains(t, prompt, "Thread: 99")
	assert.Contains(t, prompt, "Speaker: Alice")
}

func TestCodexPrompt_GroupMainChat(t *testing.T) {
	msg := channel.Message{
		Channel:    "wecom",
		ChatID:     -200,
		SenderID:   3,
		SenderName: "Bob",
		ChatType:   "group",
		Text:       "main thread msg",
	}
	prompt := codexPrompt(msg)
	assert.Contains(t, prompt, "Thread: main-chat")
}

// ── sessionScopeID tests ──

func TestSessionScopeID_ThreadIDNonZero(t *testing.T) {
	msg := channel.Message{
		Channel:  "telegram",
		ChatID:   -100,
		ThreadID: 42,
	}
	scope := sessionScopeID(msg)
	// With ThreadID != 0, scope is an FNV hash, not ChatID.
	assert.NotEqual(t, int64(-100), scope)
	assert.NotEqual(t, int64(0), scope)
}

func TestSessionScopeID_ThreadIDZero(t *testing.T) {
	msg := channel.Message{
		Channel:  "telegram",
		ChatID:   55,
		ThreadID: 0,
	}
	assert.Equal(t, int64(55), sessionScopeID(msg))
}

func TestSessionScopeID_DifferentChannelsSameChat(t *testing.T) {
	msg1 := channel.Message{Channel: "telegram", ChatID: 1, ThreadID: 10}
	msg2 := channel.Message{Channel: "wecom", ChatID: 1, ThreadID: 10}
	assert.NotEqual(t, sessionScopeID(msg1), sessionScopeID(msg2))
}

// ── HandleCardEvent tests ──

func TestHandleCardEvent_SessionsSwitch(t *testing.T) {
	c := &codex.Client{
		WorkDir: t.TempDir(),
		Timeout: time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}
	fullID := "019cc781-6b9f-7362-ad33-8fc36f7661dd"
	c.Store.Activate(1, fullID, "test session")

	svc := New(c, 1, "partial")
	msg := channel.Message{ChatID: 1, Channel: "wecom"}

	card := svc.HandleCardEvent(context.Background(), msg, "/sessions:switch", fullID)
	// After switching, GetSessionID should be updated.
	assert.Equal(t, fullID, c.GetSessionID(1))
	_ = card // card may or may not be nil depending on impl
}

func TestHandleCardEvent_SessionsNew(t *testing.T) {
	c := &codex.Client{
		WorkDir: t.TempDir(),
		Timeout: time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}
	c.SetSession(1, "old-session-id")
	svc := New(c, 1, "partial")
	msg := channel.Message{ChatID: 1, Channel: "wecom"}

	card := svc.HandleCardEvent(context.Background(), msg, "/sessions:new", "")
	require.NotNil(t, card)
	assert.Equal(t, "New Session", card.Title)
	// Session should be cleared.
	assert.Equal(t, "", c.GetSessionID(1))
}

func TestHandleCardEvent_UnknownEventKey(t *testing.T) {
	svc := New(&codex.Client{}, 1, "partial")
	msg := channel.Message{ChatID: 1}
	card := svc.HandleCardEvent(context.Background(), msg, "/unknown:event", "")
	assert.Nil(t, card)
}

func TestHandleCardEvent_SessionsHelp(t *testing.T) {
	c := &codex.Client{
		WorkDir: t.TempDir(),
		Timeout: time.Second,
	}
	svc := New(c, 1, "partial")
	msg := channel.Message{ChatID: 1, Channel: "wecom"}

	card := svc.HandleCardEvent(context.Background(), msg, "/sessions:help", "")
	require.NotNil(t, card)
	assert.Equal(t, "Help", card.Title)
}

func TestHandleCardEvent_SessionsStatus(t *testing.T) {
	c := &codex.Client{
		WorkDir: t.TempDir(),
		Timeout: time.Second,
	}
	svc := New(c, 1, "partial")
	msg := channel.Message{ChatID: 1, Channel: "wecom"}

	card := svc.HandleCardEvent(context.Background(), msg, "/sessions:status", "")
	require.NotNil(t, card)
	assert.Equal(t, "Status", card.Title)
}

func TestHandleCardEvent_SessionsSessions(t *testing.T) {
	c := &codex.Client{
		WorkDir: t.TempDir(),
		Timeout: time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}
	c.Store.Activate(1, "019cc781-6b9f-7362-ad33-8fc36f7661dd", "test")
	svc := New(c, 1, "partial")
	msg := channel.Message{ChatID: 1, Channel: "wecom"}

	card := svc.HandleCardEvent(context.Background(), msg, "/sessions:sessions", "")
	require.NotNil(t, card)
	assert.Equal(t, "🧵 Sessions", card.Title)
}

// ── New defaults ──

func TestNew_DefaultStreaming(t *testing.T) {
	svc := New(&codex.Client{}, 1, "")
	assert.Equal(t, "partial", svc.streaming)
}

func TestNew_CustomStreaming(t *testing.T) {
	svc := New(&codex.Client{}, 1, "off")
	assert.Equal(t, "off", svc.streaming)
}

// ── resolveSandbox tests ──

func TestResolveSandbox_PrivateChat(t *testing.T) {
	svc := New(&codex.Client{Sandbox: "workspace-write", GroupSandbox: "read-only"}, 1, "partial")
	msg := channel.Message{ChatType: ""}
	assert.Equal(t, "workspace-write", svc.resolveSandbox(msg))
}

func TestResolveSandbox_GroupChat(t *testing.T) {
	svc := New(&codex.Client{Sandbox: "workspace-write", GroupSandbox: "read-only"}, 1, "partial")
	msg := channel.Message{ChatType: "group"}
	assert.Equal(t, "read-only", svc.resolveSandbox(msg))
}

func TestResolveSandbox_GroupNoGroupSandbox(t *testing.T) {
	svc := New(&codex.Client{Sandbox: "workspace-write", GroupSandbox: ""}, 1, "partial")
	msg := channel.Message{ChatType: "group"}
	assert.Equal(t, "workspace-write", svc.resolveSandbox(msg))
}

// ── replyWithMediaDetection fallback ──

func TestReplyWithMediaDetection_NoMedia(t *testing.T) {
	resp := &mockResponder{}
	svc := New(&codex.Client{}, 1, "partial")
	svc.replyWithMediaDetection(
		context.Background(),
		job{
			msg:       channel.Message{Channel: "test", ChatID: 1},
			responder: resp,
		},
		"simple text reply",
	)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.Len(t, resp.replies, 1)
	assert.Equal(t, "simple text reply", resp.replies[0].Text)
}

func TestReplyWithMediaDetection_MediaFallbackToText(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "doc.pdf")
	require.NoError(t, os.WriteFile(testFile, []byte("pdf data"), 0o644))

	resp := &mockMediaResponder{
		mockResponder: mockResponder{
			replyFn: func(ctx context.Context, msg channel.Message, text string) error {
				return nil
			},
		},
	}
	// Override ReplyWithMedia to fail, triggering fallback.
	failResp := &failMediaResponder{mockMediaResponder: resp}

	svc := New(&codex.Client{}, 1, "partial")
	svc.replyWithMediaDetection(
		context.Background(),
		job{
			msg:       channel.Message{Channel: "test", ChatID: 1},
			responder: failResp,
		},
		"Here is the file: "+testFile,
	)

	// Should have fallen back to text reply.
	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.Len(t, resp.replies, 1)
	assert.Contains(t, resp.replies[0].Text, testFile)
}

type failMediaResponder struct {
	*mockMediaResponder
}

func (f *failMediaResponder) ReplyWithMedia(ctx context.Context, msg channel.Message, caption string, filePaths []string) error {
	return errors.New("media send failed")
}

func (f *failMediaResponder) SuppressTextWithMedia() bool {
	return false
}

// ── Additional coverage tests (appended) ──

func TestRun_CancelCommand_Stop(t *testing.T) {
	resp := &mockResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "/stop",
			}, resp)
			time.Sleep(200 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
	}, 2, "off")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.Len(t, resp.replies, 1)
	assert.Equal(t, "No running task to cancel.", resp.replies[0].Text)
}

func TestReplyWithMediaDetection_WithExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "output.wav")
	require.NoError(t, os.WriteFile(testFile, []byte("audio data"), 0o644))

	resp := &mockMediaResponder{}
	svc := New(&codex.Client{}, 1, "partial")
	svc.replyWithMediaDetection(
		context.Background(),
		job{
			msg:       channel.Message{Channel: "test", ChatID: 1},
			responder: resp,
		},
		"Generated audio at "+testFile+" for you.",
	)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.NotNil(t, resp.mediaCall)
	require.Len(t, resp.mediaCall.FilePaths, 1)
	assert.Equal(t, testFile, resp.mediaCall.FilePaths[0])
	// Caption should include the full text when suppress is false
	assert.Contains(t, resp.mediaCall.Caption, testFile)
}

func TestSendMediaIfPresent_NoMediaResponder(t *testing.T) {
	// Regular responder (not MediaResponder) should not panic.
	resp := &mockResponder{}
	svc := New(&codex.Client{}, 1, "partial")
	// Should not panic
	svc.sendMediaIfPresent(
		context.Background(),
		job{
			msg:       channel.Message{Channel: "test", ChatID: 1},
			responder: resp,
		},
		"/tmp/some-file.png",
	)
}

func TestSendMediaIfPresent_WithMediaFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "result.png")
	require.NoError(t, os.WriteFile(testFile, []byte("png"), 0o644))

	resp := &mockMediaResponder{}
	svc := New(&codex.Client{}, 1, "partial")
	svc.sendMediaIfPresent(
		context.Background(),
		job{
			msg:       channel.Message{Channel: "test", ChatID: 1},
			responder: resp,
		},
		"Here is the result: "+testFile,
	)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.NotNil(t, resp.mediaCall)
	assert.Empty(t, resp.mediaCall.Caption) // sendMediaIfPresent always sends empty caption
	require.Len(t, resp.mediaCall.FilePaths, 1)
	assert.Equal(t, testFile, resp.mediaCall.FilePaths[0])
}

func TestRun_DraftStreamingFallbackWhenNoChunks(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// Codex returns empty text (no stream chunks generated).
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"sess-empty-draft"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":""}}'
echo '{"type":"turn.completed","usage":{"input_tokens":10}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	resp := &mockDraftResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "do nothing",
			}, resp)
			time.Sleep(500 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 5 * time.Second,
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()

	// No drafts sent (no chunks) → should fall back to Reply.
	assert.Empty(t, resp.draftCalls)
	assert.NotEmpty(t, resp.mockStreamResponder.mockResponder.replies)
}

func TestRun_SessionsCommand(t *testing.T) {
	resp := &mockResponder{}

	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "/sessions",
			}, resp)
			time.Sleep(300 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.Len(t, resp.replies, 1)
	// /sessions with empty store should show "no sessions" or a sessions header
	assert.NotEmpty(t, resp.replies[0].Text)
}

func TestExtractFilePaths_EmptyText(t *testing.T) {
	paths := extractFilePaths("")
	assert.Empty(t, paths)
}

func TestExtractFilePaths_NoMatchingPaths(t *testing.T) {
	paths := extractFilePaths("This text has no file paths at all")
	assert.Empty(t, paths)
}

func TestNormalizeDetectedPath_Empty(t *testing.T) {
	result := normalizeDetectedPath("")
	assert.Equal(t, "", result)
}

func TestNormalizeDetectedPath_WhitespaceOnly(t *testing.T) {
	result := normalizeDetectedPath("   ")
	assert.Equal(t, "", result)
}

func TestNormalizeDetectedPath_TrimsPunctuation(t *testing.T) {
	result := normalizeDetectedPath("/tmp/file.txt。")
	assert.Equal(t, "/tmp/file.txt", result)
}

func TestExpandHomePath_NonHomePath(t *testing.T) {
	assert.Equal(t, "/absolute/path", expandHomePath("/absolute/path"))
	assert.Equal(t, "relative/path", expandHomePath("relative/path"))
}

func TestExpandHomePath_HomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	result := expandHomePath("~/subdir/file.txt")
	assert.Equal(t, filepath.Join(home, "subdir/file.txt"), result)
}

func TestStripCodeSpans_FencedBlock(t *testing.T) {
	text := "before ```code block\nwith /tmp/file.txt\n``` after"
	result := stripCodeSpans(text)
	assert.NotContains(t, result, "/tmp/file.txt")
	assert.Contains(t, result, "before")
	assert.Contains(t, result, "after")
}

func TestStripCodeSpans_InlineCode(t *testing.T) {
	text := "try running `cat /tmp/file.txt` now"
	result := stripCodeSpans(text)
	assert.NotContains(t, result, "/tmp/file.txt")
	assert.Contains(t, result, "try running")
}

// ── Additional coverage tests for gateway service ──

func TestNormalizeDetectedPath_AbsolutePath(t *testing.T) {
	result := normalizeDetectedPath("/home/user/file.txt")
	assert.Equal(t, "/home/user/file.txt", result)
}

func TestNormalizeDetectedPath_TrailingDot(t *testing.T) {
	result := normalizeDetectedPath("/tmp/output.pdf.")
	assert.Equal(t, "/tmp/output.pdf", result)
}

func TestNormalizeDetectedPath_HomePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	result := normalizeDetectedPath("~/docs/readme.md")
	assert.Contains(t, result, "docs/readme.md")
	assert.NotContains(t, result, "~")
}

func TestExpandHomePath_Empty(t *testing.T) {
	assert.Equal(t, "", expandHomePath(""))
}

func TestExpandHomePath_JustTilde(t *testing.T) {
	// "~" without "/" doesn't expand.
	assert.Equal(t, "~", expandHomePath("~"))
}

func TestStripCodeSpans_MultipleFenced(t *testing.T) {
	text := "Output:\n```\n/tmp/a.txt\n```\nand also:\n```\n/tmp/b.txt\n```\ndone"
	result := stripCodeSpans(text)
	assert.NotContains(t, result, "/tmp/a.txt")
	assert.NotContains(t, result, "/tmp/b.txt")
	assert.Contains(t, result, "Output:")
	assert.Contains(t, result, "done")
}

func TestStripCodeSpans_NoCode(t *testing.T) {
	text := "just plain text with /tmp/file.txt"
	result := stripCodeSpans(text)
	assert.Contains(t, result, "/tmp/file.txt")
}

func TestExtractFilePaths_AbsolutePath(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "output.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hi"), 0o644))

	text := "I created the file at " + testFile + " for you."
	paths := extractFilePaths(text)
	assert.Contains(t, paths, testFile)
}

func TestExtractFilePaths_InsideCodeBlock(t *testing.T) {
	dir := t.TempDir()
	testFile := filepath.Join(dir, "output.txt")
	require.NoError(t, os.WriteFile(testFile, []byte("hi"), 0o644))

	text := "```\n" + testFile + "\n```"
	paths := extractFilePaths(text)
	// Paths inside code blocks should be stripped.
	assert.Empty(t, paths)
}

func TestSplitByRuneLimit_SingleRune(t *testing.T) {
	parts := splitByRuneLimit("a", 1)
	assert.Equal(t, []string{"a"}, parts)
}

func TestSplitByRuneLimit_LargeLimit(t *testing.T) {
	text := "short"
	parts := splitByRuneLimit(text, 9999)
	assert.Equal(t, []string{"short"}, parts)
}

func TestHandleCardEvent_SessionsSwitch_Activate(t *testing.T) {
	c := &codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}
	c.Store.Activate(1, "session-xyz", "hello prompt")

	svc := New(c, 1, "partial")
	msg := channel.Message{ChatID: 1}
	card := svc.HandleCardEvent(context.Background(), msg, "/sessions:switch", "session-xyz")
	// Should return a session card and activate the session.
	assert.Equal(t, "session-xyz", c.GetSessionID(1))
	_ = card
}

func TestHandleCardEvent_EmptySelectedID(t *testing.T) {
	c := &codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}
	svc := New(c, 1, "partial")

	msg := channel.Message{ChatID: 1}
	card := svc.HandleCardEvent(context.Background(), msg, "/sessions:switch", "")
	// Empty selectedID for switch should still not panic.
	_ = card
}

func TestRun_HelpCommand(t *testing.T) {
	resp := &mockResponder{}
	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "/help",
			}, resp)
			time.Sleep(300 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.NotEmpty(t, resp.replies)
	assert.Contains(t, resp.replies[0].Text, "/help")
}

func TestRun_NewCommand(t *testing.T) {
	resp := &mockResponder{}
	driver := &mockDriver{
		name: "test",
		startFn: func(ctx context.Context, h channel.Handler) error {
			h.Handle(ctx, channel.Message{
				Channel: "test", ChatID: 1, MessageID: 1, Text: "/new",
			}, resp)
			time.Sleep(300 * time.Millisecond)
			return nil
		},
	}

	svc := New(&codex.Client{
		WorkDir: t.TempDir(),
		Timeout: 2 * time.Second,
		Store:   codex.NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}, 1, "partial")

	err := svc.Run(context.Background(), driver)
	assert.NoError(t, err)

	resp.mu.Lock()
	defer resp.mu.Unlock()
	require.NotEmpty(t, resp.replies)
	// /new resets the session - message should contain relevant response
	assert.NotEmpty(t, resp.replies[0].Text)
}
