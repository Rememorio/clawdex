package codex

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestCodexClient creates a Client with a temp SessionStore for testing.
func newTestCodexClient(t *testing.T) *Client {
	t.Helper()
	return &Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,

		Store: NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}
}

func TestRun_WithOutputFile(t *testing.T) {
	// Create a fake "codex" script that writes to the -o file.
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// The script parses -o flag and writes "hello from codex" to it.
	scriptContent := `#!/bin/sh
while [ $# -gt 0 ]; do
  case "$1" in
    -o) shift; echo "hello from codex" > "$1"; shift;;
    *) shift;;
  esac
done
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := newTestCodexClient(t)

	result := c.Run(context.Background(), 1, "test prompt", nil, "", "")
	assert.Equal(t, "hello from codex", result)
}

func TestRun_FallbackToStdout(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// Script ignores -o and just prints to stdout.
	scriptContent := `#!/bin/sh
echo "stdout output"
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := newTestCodexClient(t)

	result := c.Run(context.Background(), 1, "test prompt", nil, "", "")
	assert.Equal(t, "stdout output", result)
}

func TestRun_EmptyResponse(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := "#!/bin/sh\n"
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := newTestCodexClient(t)

	result := c.Run(context.Background(), 1, "test", nil, "", "")
	assert.Equal(t, "(empty response)", result)
}

func TestRun_Timeout(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := "#!/bin/sh\nwhile true; do :; done\n"
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := &Client{
		WorkDir: t.TempDir(),
		Timeout: 500 * time.Millisecond,

		Store: NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}

	result := c.Run(context.Background(), 1, "test", nil, "", "")
	assert.Equal(t, "codex command timeout", result)
}

func TestRun_CommandFailure(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := "#!/bin/sh\necho 'some error' >&2\nexit 1\n"
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := newTestCodexClient(t)

	result := c.Run(context.Background(), 1, "test", nil, "", "")
	assert.Contains(t, result, "some error")
}

func TestRun_TraceLogCapturesStdoutAndStderr(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := "#!/bin/sh\n" +
		"echo '{\"type\":\"thread.started\",\"thread_id\":\"trace-1\"}'\n" +
		"echo '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"ok\"}}'\n" +
		"echo 'stderr line' >&2\n"
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	tracePath := filepath.Join(t.TempDir(), "codex.log")
	traceFile, err := os.Create(tracePath)
	require.NoError(t, err)

	c := newTestCodexClient(t)
	c.Trace = NewTraceLogger(traceFile)

	result := c.Run(context.Background(), 1, "test", nil, "", "")
	require.NoError(t, traceFile.Close())
	assert.Equal(t, "ok", result)

	data, err := os.ReadFile(tracePath)
	require.NoError(t, err)
	logText := string(data)
	assert.Contains(t, logText, "codex exec started")
	assert.Contains(t, logText, "codex stdout")
	assert.Contains(t, logText, "trace-1")
	assert.Contains(t, logText, "codex stderr")
	assert.Contains(t, logText, "stderr line")
}

func TestRun_CommandNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	c := newTestCodexClient(t)

	result := c.Run(context.Background(), 1, "test", nil, "", "")
	assert.Contains(t, result, "codex failed:")
	assert.Contains(t, result, "codex executable not found in PATH")
	assert.Contains(t, result, "~/.clawdex/env")
}

func TestRun_SandboxFlag(t *testing.T) {
	// Verify that --sandbox flag is passed to codex.
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// Script writes all args to a file for inspection.
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	scriptContent := `#!/bin/sh
echo "$@" > ` + argsFile + `
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := &Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,

		Sandbox: "workspace-write",
		Store:   NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}

	c.Run(context.Background(), 1, "test prompt", nil, "workspace-write", "")

	argsData, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Contains(t, string(argsData), "--sandbox workspace-write")
}

func TestRun_SoulContentInjected(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	scriptContent := `#!/bin/sh
echo "$@" > ` + argsFile + `
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := &Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,

		SoulContent: "You are a helpful cat.",
		Store:       NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}

	c.Run(context.Background(), 1, "hi", nil, "", "")

	argsData, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Contains(t, string(argsData), "-c model_instructions_file=")
}

func TestRun_SoulContentNotOnResume(t *testing.T) {
	// Soul instructions should only be injected on fresh exec, not resume.
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	argsFile := filepath.Join(t.TempDir(), "args.log")
	scriptContent := `#!/bin/sh
echo "$@" >> ` + argsFile + `
echo '{"type":"thread.started","thread_id":"s-1"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := &Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,

		SoulContent: "Be a cat.",
		Store:       NewSessionStore(filepath.Join(t.TempDir(), "sessions.json")),
	}

	// First call: fresh — should contain instructions.
	c.Run(context.Background(), 1, "hello", nil, "", "")
	// Second call: resume — should NOT contain instructions.
	c.Run(context.Background(), 1, "again", nil, "", "")

	argsData, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(argsData)), "\n")
	require.Len(t, lines, 2)
	assert.Contains(t, lines[0], "model_instructions_file=")
	assert.NotContains(t, lines[1], "model_instructions_file=")
}

func TestRun_JSONLParsesAgentMessage(t *testing.T) {
	// Codex emits JSONL with thread.started and item.completed.
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"sess-123"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"Hello from session"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
	c := &Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,

		Store: store,
	}

	result := c.Run(context.Background(), 42, "test", nil, "", "")
	assert.Equal(t, "Hello from session", result)

	// Verify session was activated in the store.
	assert.Equal(t, "sess-123", c.GetSessionID(42))

	// Verify Store.Activate was called.
	sessions := store.List(0, 0)
	require.Len(t, sessions, 1)
	assert.Equal(t, int64(42), sessions[0].ChatID)
	assert.Equal(t, "sess-123", sessions[0].ThreadID)
	assert.Equal(t, "test", sessions[0].Title)
	assert.True(t, sessions[0].Active)
}

func TestRun_SessionResume(t *testing.T) {
	// First call: fresh exec stores session ID.
	// Second call: should use "resume" subcommand.
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	// Script writes its first arg (exec/resume) to a file, emits JSONL.
	logFile := filepath.Join(t.TempDir(), "mode.log")
	scriptContent := `#!/bin/sh
echo "$1 $2" >> ` + logFile + `
echo '{"type":"thread.started","thread_id":"sess-abc"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"response"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
	c := &Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,

		Store: store,
	}

	// First call: fresh exec.
	result1 := c.Run(context.Background(), 100, "first message", nil, "", "")
	assert.Equal(t, "response", result1)

	// Second call: should resume.
	result2 := c.Run(context.Background(), 100, "second message", nil, "", "")
	assert.Equal(t, "response", result2)

	// Verify the modes used.
	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, "exec --json", lines[0])
	assert.Equal(t, "exec resume", lines[1])

	// Verify store has one entry (upserted), with updated title from resume.
	sessions := store.List(0, 0)
	require.Len(t, sessions, 1)
	assert.Equal(t, int64(100), sessions[0].ChatID)
	assert.Equal(t, "sess-abc", sessions[0].ThreadID)
	assert.Equal(t, "second message", sessions[0].Title)
}

func TestRun_ResumeFailureFallsBackToFresh(t *testing.T) {
	// Pre-seed a session ID, then make resume fail.
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	callFile := filepath.Join(t.TempDir(), "calls.log")
	// Script: if "resume" is arg2, fail; otherwise succeed.
	scriptContent := `#!/bin/sh
echo "$2" >> ` + callFile + `
if [ "$2" = "resume" ]; then
  exit 1
fi
echo '{"type":"thread.started","thread_id":"new-sess"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"fresh response"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
	c := &Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,

		Store: store,
	}
	// Pre-seed a session.
	c.SetSession(200, "old-sess")

	result := c.Run(context.Background(), 200, "test", nil, "", "")
	assert.Equal(t, "fresh response", result)
	// Session should be updated to new one.
	assert.Equal(t, "new-sess", c.GetSessionID(200))

	// Verify resume was attempted then fresh exec ran.
	callData, err := os.ReadFile(callFile)
	require.NoError(t, err)
	calls := strings.Split(strings.TrimSpace(string(callData)), "\n")
	require.Len(t, calls, 2)
	assert.Equal(t, "resume", calls[0])
	assert.Equal(t, "--json", calls[1])
}

func TestParseJSONL_ThreadAndMessage(t *testing.T) {
	data := `{"type":"thread.started","thread_id":"t-1"}
{"type":"item.completed","item":{"type":"tool_call","text":"ignored"}}
{"type":"item.completed","item":{"type":"agent_message","text":"final answer"}}
`
	threadID, agentText := parseJSONL([]byte(data))
	assert.Equal(t, "t-1", threadID)
	assert.Equal(t, "final answer", agentText)
}

func TestParseJSONL_Empty(t *testing.T) {
	threadID, agentText := parseJSONL([]byte(""))
	assert.Empty(t, threadID)
	assert.Empty(t, agentText)
}

func TestParseJSONL_GarbageLines(t *testing.T) {
	data := "not json\n{\"type\":\"thread.started\",\"thread_id\":\"t-2\"}\nmore garbage\n"
	threadID, agentText := parseJSONL([]byte(data))
	assert.Equal(t, "t-2", threadID)
	assert.Empty(t, agentText)
}

func TestParseJSONL_MultipleAgentMessages(t *testing.T) {
	// Should pick the last agent_message.
	data := `{"type":"item.completed","item":{"type":"agent_message","text":"first"}}
{"type":"item.completed","item":{"type":"agent_message","text":"last"}}
`
	_, agentText := parseJSONL([]byte(data))
	assert.Equal(t, "last", agentText)
}

func TestRun_ImageFlag(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	scriptContent := `#!/bin/sh
echo "$@" > ` + argsFile + `
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := newTestCodexClient(t)

	c.Run(context.Background(), 1, "describe these images", []string{"/tmp/a.jpg", "/tmp/b.png"}, "", "")

	argsData, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	args := string(argsData)
	assert.Contains(t, args, "--image /tmp/a.jpg")
	assert.Contains(t, args, "--image /tmp/b.png")
}

// ── CleanEnv tests ──

func TestCleanEnv_StripsOpenAIVars(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "http://proxy.example.com")
	t.Setenv("OPENAI_API_KEY", "sk-test-key")
	t.Setenv("OTHER_VAR", "keep-me")

	env := CleanEnv()

	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		assert.NotEqual(t, "OPENAI_BASE_URL", key)
		assert.NotEqual(t, "OPENAI_API_KEY", key)
	}

	found := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "OTHER_VAR=") {
			found = true
			break
		}
	}
	assert.True(t, found, "OTHER_VAR should be preserved")
}

func TestCleanEnv_NoOpenAIVars(t *testing.T) {
	// Unset to ensure they're not present
	os.Unsetenv("OPENAI_BASE_URL")
	os.Unsetenv("OPENAI_API_KEY")

	env := CleanEnv()
	assert.NotEmpty(t, env) // should still have other env vars
}

func TestHasSession(t *testing.T) {
	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
	c := &Client{Store: store}
	assert.False(t, c.HasSession(1))

	c.SetSession(1, "sess-1")
	assert.True(t, c.HasSession(1))

	c.ResetSession(1)
	assert.False(t, c.HasSession(1))
}

// ── RunStream tests ──

func TestRunStream_CallsCallback(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"stream-1"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"chunk one"}}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"chunk two"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
	c := &Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,

		Store: store,
	}

	var chunks []string
	result := c.RunStream(context.Background(), 1, "test", nil, func(text string) {
		chunks = append(chunks, text)
	}, "", "")

	// RunStream returns only the last agent message.
	assert.Equal(t, "chunk two", result)
	// Streaming chunks accumulate across agent turns.
	require.Len(t, chunks, 2)
	assert.Equal(t, "chunk one", chunks[0])
	assert.Equal(t, "chunk one\n\n---\n\nchunk two", chunks[1])
	assert.Equal(t, "stream-1", c.GetSessionID(1))
}

func TestRunStream_ReturnsLastAgentMessage(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"item.completed","item":{"type":"agent_message","text":"first"}}'
echo '{"type":"item.completed","item":{"type":"tool_call","text":"ignored"}}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"final answer"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := newTestCodexClient(t)

	result := c.RunStream(context.Background(), 1, "test", nil, func(text string) {}, "", "")
	assert.Equal(t, "final answer", result)
}

func TestRunStream_NilCallback(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"item.completed","item":{"type":"agent_message","text":"response"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	c := newTestCodexClient(t)

	// nil callback should not panic.
	result := c.RunStream(context.Background(), 1, "test", nil, nil, "", "")
	assert.Equal(t, "response", result)
}

func TestRunStream_SessionResume(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	logFile := filepath.Join(t.TempDir(), "mode.log")
	scriptContent := `#!/bin/sh
echo "$1 $2" >> ` + logFile + `
echo '{"type":"thread.started","thread_id":"sess-abc"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"response"}}'
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
	c := &Client{
		WorkDir: t.TempDir(),
		Timeout: 10 * time.Second,

		Store: store,
	}

	// First: fresh exec.
	c.RunStream(context.Background(), 100, "first", nil, nil, "", "")
	// Second: resume.
	c.RunStream(context.Background(), 100, "second", nil, nil, "", "")

	logData, err := os.ReadFile(logFile)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, "exec --json", lines[0])
	assert.Equal(t, "exec resume", lines[1])
}

func TestRunStream_TimeoutPreservesStartedSession(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "codex")
	scriptContent := `#!/bin/sh
echo '{"type":"thread.started","thread_id":"stream-timeout"}'
sleep 2
`
	require.NoError(t, os.WriteFile(script, []byte(scriptContent), 0o755))
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	store := NewSessionStore(filepath.Join(t.TempDir(), "sessions.json"))
	c := &Client{
		WorkDir: t.TempDir(),
		Timeout: 500 * time.Millisecond,
		Store:   store,
	}

	result := c.RunStream(context.Background(), 1, "test", nil, nil, "", "")
	assert.Equal(t, "codex command timeout", result)
	assert.Equal(t, "stream-timeout", c.GetSessionID(1))
}

func TestExtractErrorFromJSONL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "error and turn.failed both present",
			raw: `{"type":"thread.started","thread_id":"019ded50-b417-7702-8973-f2f80971ee3c"}
{"type":"turn.started"}
{"type":"error","message":"Your access token could not be refreshed because your refresh token was already used. Please log out and sign in again."}
{"type":"turn.failed","error":{"message":"Your access token could not be refreshed because your refresh token was already used. Please log out and sign in again."}}`,
			want: "Your access token could not be refreshed because your refresh token was already used. Please log out and sign in again.",
		},
		{
			name: "only turn.failed present",
			raw: `{"type":"thread.started","thread_id":"abc"}
{"type":"turn.started"}
{"type":"turn.failed","error":{"message":"Usage limit exceeded."}}`,
			want: "Usage limit exceeded.",
		},
		{
			name: "only error event present",
			raw: `{"type":"thread.started","thread_id":"abc"}
{"type":"error","message":"Rate limited."}`,
			want: "Rate limited.",
		},
		{
			name: "no error events",
			raw: `{"type":"thread.started","thread_id":"abc"}
{"type":"turn.started"}`,
			want: "",
		},
		{
			name: "empty input",
			raw:  "",
			want: "",
		},
		{
			name: "non-json garbage",
			raw:  "not valid json at all",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractErrorFromJSONL(tt.raw)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLooksLikeJSONL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"valid jsonl", `{"type":"thread.started"}`, true},
		{"with whitespace", `  {"type":"event"}`, true},
		{"empty", "", false},
		{"plain text", "hello world", false},
		{"starts with bracket", "[1,2,3]", false},
		{"multiline jsonl", "{\"type\":\"a\"}\n{\"type\":\"b\"}", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, looksLikeJSONL(tt.raw))
		})
	}
}

func TestResolveOutput_EmptyAgentText_JSONL(t *testing.T) {
	// When codex returns valid JSONL but agent text is empty (e.g. tool call
	// produced no visible output), resolveOutput should NOT dump raw JSON.
	c := &Client{Timeout: time.Minute}
	rawJSONL := `{"type":"thread.started","thread_id":"t-1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":""}}
{"type":"turn.completed","usage":{"input_tokens":100}}
`
	// lastMsgPath points to a file that doesn't exist → won't be read.
	result := c.resolveOutput(context.Background(), "", rawJSONL, "/nonexistent/path", nil)
	assert.Equal(t, "Sorry, I was unable to generate a response for this request.", result)
	assert.NotContains(t, result, "thread.started")
}

func TestResolveOutput_EmptyAgentText_WithError(t *testing.T) {
	c := &Client{Timeout: time.Minute}
	rawJSONL := `{"type":"thread.started","thread_id":"t-1"}
{"type":"turn.failed","error":{"message":"rate limit exceeded"}}
`
	result := c.resolveOutput(context.Background(), "", rawJSONL, "/nonexistent/path", nil)
	// Should extract the error, not dump raw JSON.
	assert.Equal(t, "rate limit exceeded", result)
}

func TestResolveOutput_PrefersAgentText(t *testing.T) {
	c := &Client{Timeout: time.Minute}
	result := c.resolveOutput(context.Background(), "hello from agent", `{"raw":"json"}`, "/nonexistent", nil)
	assert.Equal(t, "hello from agent", result)
}

func TestResolveOutput_FallsBackToOutputFile(t *testing.T) {
	c := &Client{Timeout: time.Minute}
	tmpFile := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("file content"), 0o644))

	result := c.resolveOutput(context.Background(), "", `{"type":"thread.started"}`, tmpFile, nil)
	assert.Equal(t, "file content", result)
}

func TestResolveOutput_PlainTextFallback(t *testing.T) {
	c := &Client{Timeout: time.Minute}
	// Non-JSONL raw output (plain text) should still be returned as-is.
	result := c.resolveOutput(context.Background(), "", "plain output from stderr", "/nonexistent", nil)
	assert.Equal(t, "plain output from stderr", result)
}

// ── Additional coverage tests ──

func TestFormatExecError_Nil(t *testing.T) {
	assert.Equal(t, "", formatExecError(nil))
}

func TestFormatExecError_NotFound(t *testing.T) {
	result := formatExecError(exec.ErrNotFound)
	assert.Contains(t, result, "codex executable not found in PATH")
}

func TestFormatExecError_NotFoundText(t *testing.T) {
	err := errors.New("exec: \"codex\": executable file not found in $PATH")
	result := formatExecError(err)
	assert.Contains(t, result, "codex executable not found in PATH")
}

func TestFormatExecError_OtherError(t *testing.T) {
	err := errors.New("something went wrong")
	result := formatExecError(err)
	assert.Equal(t, "something went wrong", result)
}

func TestResolveSoul_Default(t *testing.T) {
	c := &Client{SoulContent: "global soul"}
	assert.Equal(t, "global soul", c.resolveSoul(""))
}

func TestResolveSoul_Override(t *testing.T) {
	c := &Client{
		SoulContent:   "global soul",
		SoulOverrides: map[string]string{"telegram": "tg soul"},
	}
	assert.Equal(t, "tg soul", c.resolveSoul("telegram"))
}

func TestResolveSoul_OverrideMissing(t *testing.T) {
	c := &Client{
		SoulContent:   "global soul",
		SoulOverrides: map[string]string{"telegram": "tg soul"},
	}
	assert.Equal(t, "global soul", c.resolveSoul("wecom"))
}

func TestResolveSoul_NilOverrides(t *testing.T) {
	c := &Client{SoulContent: "global"}
	assert.Equal(t, "global", c.resolveSoul("anything"))
}

func TestTraceCommandName(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"empty", nil, ""},
		{"single", []string{"exec"}, "exec"},
		{"two args", []string{"exec", "resume"}, "exec resume"},
		{"three args", []string{"exec", "resume", "--json"}, "exec resume"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, traceCommandName(tt.args))
		})
	}
}

func TestResumeSandboxArgs(t *testing.T) {
	tests := []struct {
		name    string
		sandbox string
		want    []string
	}{
		{"danger-full-access", "danger-full-access", []string{"--dangerously-bypass-approvals-and-sandbox"}},
		{"workspace-write", "workspace-write", []string{"--full-auto"}},
		{"read-only", "read-only", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resumeSandboxArgs(tt.sandbox)
			assert.Equal(t, tt.want, result)
		})
	}
}

func TestNewTraceLogger_Nil(t *testing.T) {
	tl := NewTraceLogger(nil)
	assert.Nil(t, tl)
}

func TestNewTraceLogger_Valid(t *testing.T) {
	var buf strings.Builder
	tl := NewTraceLogger(&buf)
	require.NotNil(t, tl)
	tl.Log("test message", "key", "value")
	assert.Contains(t, buf.String(), "test message")
}

func TestTraceLogger_Log_Nil(t *testing.T) {
	var tl *TraceLogger
	// Should not panic.
	tl.Log("message")
}

func TestExecutableName_Default(t *testing.T) {
	c := &Client{}
	assert.Equal(t, "codex", c.executableName())
}

func TestExecutableName_Override(t *testing.T) {
	c := &Client{Executable: "/usr/local/bin/my-codex"}
	assert.Equal(t, "/usr/local/bin/my-codex", c.executableName())
}

func TestCleanEnv_StripsMultipleVars(t *testing.T) {
	t.Setenv("OPENAI_BASE_URL", "http://proxy.example.com")
	t.Setenv("OPENAI_API_KEY", "sk-fake-key")

	env := CleanEnv()
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		assert.NotEqual(t, "OPENAI_BASE_URL", key)
		assert.NotEqual(t, "OPENAI_API_KEY", key)
	}
}

func TestResolveOutput_ExecError(t *testing.T) {
	c := &Client{Timeout: time.Minute}
	err := errors.New("exit status 1")
	result := c.resolveOutput(context.Background(), "", "", "/nonexistent", err)
	assert.Contains(t, result, "exit status 1")
}

func TestResolveOutput_ContextTimeout(t *testing.T) {
	c := &Client{Timeout: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Already cancelled.
	result := c.resolveOutput(ctx, "", "", "/nonexistent", context.DeadlineExceeded)
	assert.NotEmpty(t, result)
}
