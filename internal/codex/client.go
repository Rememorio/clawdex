// Package codex wraps native Codex CLI execution for gateway requests.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// envKeysToStrip are environment variables that must be removed from the codex
// subprocess. When clawdex runs inside an environment that sets these to a
// local proxy (e.g. venus), codex would fail with 403 because the proxy does
// not recognise the credentials.
var envKeysToStrip = map[string]bool{
	"OPENAI_BASE_URL": true,
	"OPENAI_API_KEY":  true,
}

// CleanEnv returns a copy of os.Environ() with proxy-related OpenAI variables
// removed so the codex CLI uses its own defaults.
func CleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if envKeysToStrip[key] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

const (
	codexExecutableName     = "codex"
	execErrNotFoundText     = "executable file not found in $PATH"
	traceScannerInitialSize = 64 * 1024
	traceScannerMaxSize     = 1024 * 1024

	// cancelGracePeriod is how long we wait after SIGINT before force-killing.
	cancelGracePeriod = 5 * time.Second
)

// setGracefulCancel configures cmd to send SIGINT (instead of SIGKILL) when
// the context is cancelled, giving codex time to persist session state.
func setGracefulCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGINT)
	}
	cmd.WaitDelay = cancelGracePeriod
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// TraceLogger writes detailed Codex execution traces.
type TraceLogger struct {
	logger *slog.Logger
}

// NewTraceLogger constructs a trace logger for Codex execution details.
func NewTraceLogger(w io.Writer) *TraceLogger {
	if w == nil {
		return nil
	}
	handler := slog.NewTextHandler(&syncWriter{w: w}, nil)
	return &TraceLogger{logger: slog.New(handler)}
}

func (t *TraceLogger) Log(msg string, args ...any) {
	if t == nil || t.logger == nil {
		return
	}
	t.logger.Info(msg, args...)
}

func newTraceScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, traceScannerInitialSize), traceScannerMaxSize)
	return scanner
}

// jsonEvent represents a single JSONL event emitted by `codex exec --json`.
type jsonEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Message  string          `json:"message,omitempty"`
	Error    *jsonEventError `json:"error,omitempty"`
	Item     *jsonItem       `json:"item,omitempty"`
}

// jsonEventError carries the error payload inside turn.failed events.
type jsonEventError struct {
	Message string `json:"message,omitempty"`
}

// jsonItem is the nested item payload inside item.started/item.completed events.
type jsonItem struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Command string `json:"command,omitempty"`
	Status  string `json:"status,omitempty"`
}

// Client executes prompts through `codex exec` and returns final text output.
type Client struct {
	WorkDir       string
	Timeout       time.Duration
	Sandbox       string
	GroupSandbox  string
	SoulContent   string            // SOUL.md content, injected via -c instructions on fresh sessions
	SoulOverrides map[string]string // channel name → soul content; overrides SoulContent per channel
	Store         *SessionStore
	Trace         *TraceLogger
	Executable    string // override for the codex binary path (default: "codex")
}

// executableName returns the codex binary name to use.
func (c *Client) executableName() string {
	if c.Executable != "" {
		return c.Executable
	}
	return codexExecutableName
}

// resolveSoul returns the soul content for the given channel.
// Per-channel overrides take priority; falls back to the global SoulContent.
func (c *Client) resolveSoul(channel string) string {
	if channel != "" && c.SoulOverrides != nil {
		if content, ok := c.SoulOverrides[channel]; ok {
			return content
		}
	}
	return c.SoulContent
}

// ResetSession marks the active session for the given chat as inactive,
// so the next message starts a fresh codex exec.
func (c *Client) ResetSession(chatID int64) {
	c.Store.Deactivate(chatID)
}

// HasSession reports whether the given chat has an active session.
func (c *Client) HasSession(chatID int64) bool {
	return c.Store.ActiveSession(chatID) != ""
}

// SetSession activates the given session for the chat,
// so the next message resumes that session.
func (c *Client) SetSession(chatID int64, sessionID string) {
	c.Store.Activate(chatID, sessionID, "")
}

// GetSessionID returns the current active session ID for the given chat, or "".
func (c *Client) GetSessionID(chatID int64) string {
	return c.Store.ActiveSession(chatID)
}

func (c *Client) trace(msg string, args ...any) {
	if c == nil || c.Trace == nil {
		return
	}
	c.Trace.Log(msg, args...)
}

func traceArgs(base []any, extra ...any) []any {
	args := make([]any, 0, len(base)+len(extra))
	args = append(args, base...)
	args = append(args, extra...)
	return args
}

func traceCommandName(args []string) string {
	switch {
	case len(args) >= 2:
		return args[0] + " " + args[1]
	case len(args) == 1:
		return args[0]
	default:
		return ""
	}
}

func formatExecError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, exec.ErrNotFound) ||
		strings.Contains(err.Error(), execErrNotFoundText) {
		return "codex executable not found in PATH. Install Codex CLI or " +
			"add its directory to PATH. If you use the systemd daemon, " +
			"set PATH in ~/.clawdex/env or reinstall the daemon."
	}
	return err.Error()
}

type traceLineWriter struct {
	mu      sync.Mutex
	raw     bytes.Buffer
	pending bytes.Buffer
	logFn   func(string)
}

func (w *traceLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := w.raw.Write(p); err != nil {
		return 0, err
	}
	if _, err := w.pending.Write(p); err != nil {
		return 0, err
	}
	w.flushLocked(false)
	return len(p), nil
}

func (w *traceLineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked(true)
}

func (w *traceLineWriter) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.raw.Len()
}

func (w *traceLineWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.raw.String()
}

func (w *traceLineWriter) flushLocked(includePartial bool) {
	for {
		data := w.pending.Bytes()
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			break
		}
		line := string(data[:idx])
		w.pending.Next(idx + 1)
		if w.logFn != nil {
			w.logFn(line)
		}
	}

	if includePartial && w.pending.Len() > 0 {
		line := w.pending.String()
		w.pending.Reset()
		if w.logFn != nil {
			w.logFn(line)
		}
	}
}

// StreamCallback is called for each chunk of streaming output.
type StreamCallback func(text string)

// RunStream forwards one prompt to Codex with streaming output via onChunk.
// Returns the final response text. channel identifies the originating channel
// for per-instance SOUL resolution.
func (c *Client) RunStream(parent context.Context, chatID int64, prompt string, imagePaths []string, onChunk StreamCallback, sandbox, channel string) string {
	ctx, cancel := context.WithTimeout(parent, c.Timeout)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "clawdex-")
	if err != nil {
		return "failed to create temporary directory: " + err.Error()
	}
	defer os.RemoveAll(tmpDir)

	lastMsgPath := filepath.Join(tmpDir, "last-message.txt")

	sessionID := c.Store.ActiveSession(chatID)
	if sessionID != "" {
		result := c.execResumeStream(ctx, sessionID, lastMsgPath, prompt, imagePaths, onChunk, sandbox)
		if result != "" {
			c.Store.Activate(chatID, sessionID, prompt)
			return result
		}
		c.Store.Deactivate(chatID)
	}

	return c.execFreshStream(ctx, chatID, lastMsgPath, prompt, imagePaths, onChunk, sandbox, channel)
}

func (c *Client) execFreshStream(ctx context.Context, chatID int64, lastMsgPath, prompt string, imagePaths []string, onChunk StreamCallback, sandbox, channel string) string {
	args := []string{"exec", "--json", "--skip-git-repo-check", "-C", c.WorkDir, "-o", lastMsgPath}
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}

	var soulFile string
	if soul := c.resolveSoul(channel); soul != "" {
		f, err := os.CreateTemp("", "clawdex-soul-*.md")
		if err == nil {
			soulFile = f.Name()
			_, _ = f.WriteString(soul)
			f.Close()
			args = append(args, "-c", "model_instructions_file="+soulFile)
		}
	}

	args = append(args, prompt)
	for _, p := range imagePaths {
		args = append(args, "--image", p)
	}

	threadID, text, rawOut, err := c.runCodexStream(ctx, args, onChunk,
		func(startedThreadID string) {
			c.Store.Activate(chatID, startedThreadID, prompt)
		})
	if soulFile != "" {
		os.Remove(soulFile)
	}
	if threadID != "" {
		c.Store.Activate(chatID, threadID, prompt)
	}
	return c.resolveOutput(ctx, text, rawOut, lastMsgPath, err)
}

func (c *Client) execResumeStream(ctx context.Context, sessionID, lastMsgPath, prompt string, imagePaths []string, onChunk StreamCallback, sandbox string) string {
	args := []string{"exec", "resume", "--json", "--skip-git-repo-check", "-o", lastMsgPath}
	args = append(args, resumeSandboxArgs(sandbox)...)
	args = append(args, sessionID, prompt)
	for _, p := range imagePaths {
		args = append(args, "--image", p)
	}

	_, text, rawOut, err := c.runCodexStream(ctx, args, onChunk, nil)
	if err != nil && text == "" && strings.TrimSpace(rawOut) == "" {
		return ""
	}
	return c.resolveOutput(ctx, text, rawOut, lastMsgPath, err)
}

// runCodexStream executes codex and streams JSONL output line-by-line.
// On each item.completed with agent_message, calls onChunk with the text.
func (c *Client) runCodexStream(ctx context.Context, args []string, onChunk StreamCallback, onThreadStarted func(string)) (string, string, string, error) {
	cmd := exec.CommandContext(ctx, c.executableName(), args...)
	cmd.Env = CleanEnv()
	setGracefulCancel(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", "", err
	}

	traceBase := []any{
		"command", traceCommandName(args),
		"workdir", c.WorkDir,
		"arg_count", len(args),
	}
	stderrBuf := &traceLineWriter{logFn: func(line string) {
		c.trace("codex stderr", traceArgs(traceBase, "line", line)...)
	}}
	cmd.Stderr = stderrBuf
	c.trace("codex exec started", traceArgs(traceBase)...)

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		c.trace("codex exec start failed",
			traceArgs(traceBase, "error", err)...)
		return "", "", "", err
	}

	var threadID, agentText string
	// streamText accumulates all agent messages so that
	// intermediate responses remain visible during streaming.
	var streamText string
	var rawBuf bytes.Buffer
	scanner := newTraceScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		rawBuf.WriteString(line)
		rawBuf.WriteByte('\n')
		c.trace("codex stdout", traceArgs(traceBase, "line", line)...)

		var ev jsonEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "thread.started" && ev.ThreadID != "" {
			threadID = ev.ThreadID
			if onThreadStarted != nil {
				onThreadStarted(threadID)
				onThreadStarted = nil
			}
		}
		if ev.Type == "item.completed" && ev.Item != nil &&
			ev.Item.Type == "agent_message" && ev.Item.Text != "" {
			agentText = ev.Item.Text
			// Accumulate multi-turn agent messages with a separator
			// so earlier responses remain visible to the user.
			if streamText == "" {
				streamText = agentText
			} else {
				streamText += "\n\n---\n\n" + agentText
			}
			if onChunk != nil {
				onChunk(streamText)
			}
		}
	}

	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	stderrBuf.Flush()
	if scanErr != nil && waitErr == nil {
		waitErr = scanErr
	}

	raw := rawBuf.String()
	if raw == "" {
		raw = stderrBuf.String()
	}
	c.trace("codex exec finished", traceArgs(
		traceBase,
		"thread_id", threadID,
		"duration_ms", time.Since(startedAt).Milliseconds(),
		"stdout_bytes", rawBuf.Len(),
		"stderr_bytes", stderrBuf.Len(),
		"error", waitErr,
	)...)
	return threadID, agentText, raw, waitErr
}

// Run forwards one prompt to Codex and returns the final response text.
// chatID is used to maintain session continuity per chat.
// imagePaths are optional local file paths for images to attach.
// channel identifies the originating channel for per-instance SOUL resolution.
func (c *Client) Run(parent context.Context, chatID int64, prompt string, imagePaths []string, sandbox, channel string) string {
	ctx, cancel := context.WithTimeout(parent, c.Timeout)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "clawdex-")
	if err != nil {
		return "failed to create temporary directory: " + err.Error()
	}
	defer os.RemoveAll(tmpDir)

	lastMsgPath := filepath.Join(tmpDir, "last-message.txt")

	sessionID := c.Store.ActiveSession(chatID)
	if sessionID != "" {
		// Try to resume existing session.
		result := c.execResume(ctx, sessionID, lastMsgPath, prompt, imagePaths, sandbox)
		if result != "" {
			c.Store.Activate(chatID, sessionID, prompt)
			return result
		}
		// Resume failed — fall through to fresh exec.
		c.Store.Deactivate(chatID)
	}

	return c.execFresh(ctx, chatID, lastMsgPath, prompt, imagePaths, sandbox, channel)
}

// execFresh runs a new `codex exec --json` invocation and stores the session ID.
func (c *Client) execFresh(ctx context.Context, chatID int64, lastMsgPath, prompt string, imagePaths []string, sandbox, channel string) string {
	args := []string{"exec", "--json", "--skip-git-repo-check", "-C", c.WorkDir, "-o", lastMsgPath}
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}

	// Write SOUL.md content to a temp file and pass via model_instructions_file.
	// Using -c instructions=<content> breaks when the content has newlines or
	// special characters because codex parses the value as TOML.
	var soulFile string
	if soul := c.resolveSoul(channel); soul != "" {
		f, err := os.CreateTemp("", "clawdex-soul-*.md")
		if err == nil {
			soulFile = f.Name()
			_, _ = f.WriteString(soul)
			f.Close()
			args = append(args, "-c", "model_instructions_file="+soulFile)
		}
	}

	// Prompt must come before --image; codex's --image <FILE>... is variadic
	// and will swallow subsequent positional args.
	args = append(args, prompt)
	for _, p := range imagePaths {
		args = append(args, "--image", p)
	}

	threadID, text, rawOut, err := c.runCodex(ctx, args,
		func(startedThreadID string) {
			c.Store.Activate(chatID, startedThreadID, prompt)
		})
	if soulFile != "" {
		os.Remove(soulFile)
	}
	if threadID != "" {
		c.Store.Activate(chatID, threadID, prompt)
	}
	return c.resolveOutput(ctx, text, rawOut, lastMsgPath, err)
}

// execResume runs `codex exec resume --json <sessionID> <prompt>`.
// Returns the response text, or "" if the resume failed.
func (c *Client) execResume(ctx context.Context, sessionID, lastMsgPath, prompt string, imagePaths []string, sandbox string) string {
	args := []string{"exec", "resume", "--json", "--skip-git-repo-check", "-o", lastMsgPath}
	args = append(args, resumeSandboxArgs(sandbox)...)
	// Positional args (sessionID, prompt) must come before --image.
	args = append(args, sessionID, prompt)
	for _, p := range imagePaths {
		args = append(args, "--image", p)
	}

	_, text, rawOut, err := c.runCodex(ctx, args, nil)
	if err != nil && text == "" && strings.TrimSpace(rawOut) == "" {
		// Resume failed with no useful output — signal caller to retry fresh.
		return ""
	}
	return c.resolveOutput(ctx, text, rawOut, lastMsgPath, err)
}

// runCodex executes the codex binary with the given args and parses JSONL output.
// Returns (threadID, agentMessageText, rawStdout, error).
func (c *Client) runCodex(ctx context.Context, args []string, onThreadStarted func(string)) (string, string, string, error) {
	cmd := exec.CommandContext(ctx, c.executableName(), args...)
	cmd.Env = CleanEnv()
	setGracefulCancel(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", "", "", err
	}

	traceBase := []any{
		"command", traceCommandName(args),
		"workdir", c.WorkDir,
		"arg_count", len(args),
	}
	stderrBuf := &traceLineWriter{logFn: func(line string) {
		c.trace("codex stderr", traceArgs(traceBase, "line", line)...)
	}}
	cmd.Stderr = stderrBuf
	c.trace("codex exec started", traceArgs(traceBase)...)

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		c.trace("codex exec start failed",
			traceArgs(traceBase, "error", err)...)
		return "", "", "", err
	}

	var threadID, agentText string
	var rawBuf bytes.Buffer
	scanner := newTraceScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		rawBuf.WriteString(line)
		rawBuf.WriteByte('\n')
		c.trace("codex stdout", traceArgs(traceBase, "line", line)...)

		var ev jsonEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		if ev.Type == "thread.started" && ev.ThreadID != "" {
			threadID = ev.ThreadID
			if onThreadStarted != nil {
				onThreadStarted(threadID)
				onThreadStarted = nil
			}
		}
		if ev.Type == "item.completed" && ev.Item != nil &&
			ev.Item.Type == "agent_message" && ev.Item.Text != "" {
			agentText = ev.Item.Text
		}
	}

	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	stderrBuf.Flush()
	if scanErr != nil && waitErr == nil {
		waitErr = scanErr
	}

	raw := rawBuf.String()
	if raw == "" {
		raw = stderrBuf.String()
	}
	c.trace("codex exec finished", traceArgs(
		traceBase,
		"thread_id", threadID,
		"duration_ms", time.Since(startedAt).Milliseconds(),
		"stdout_bytes", rawBuf.Len(),
		"stderr_bytes", stderrBuf.Len(),
		"error", waitErr,
	)...)

	return threadID, agentText, raw, waitErr
}

// parseJSONL scans JSONL bytes for the thread_id and last agent_message text.
func parseJSONL(data []byte) (threadID string, agentText string) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev jsonEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Type == "thread.started" && ev.ThreadID != "" {
			threadID = ev.ThreadID
		}
		if ev.Type == "item.completed" && ev.Item != nil && ev.Item.Type == "agent_message" && ev.Item.Text != "" {
			agentText = ev.Item.Text
		}
	}
	return
}

// resolveOutput picks the best available response: JSONL agent text, -o file, or raw stdout.
func (c *Client) resolveOutput(ctx context.Context, jsonText, rawOut, lastMsgPath string, execErr error) string {
	// Prefer the JSONL-parsed agent message.
	if jsonText != "" {
		return jsonText
	}

	// Fall back to the -o output file.
	if last, readErr := os.ReadFile(lastMsgPath); readErr == nil {
		text := strings.TrimSpace(string(last))
		if text != "" {
			return text
		}
	}

	// If rawOut is valid JSONL (codex ran successfully but produced no text),
	// don't dump raw JSON to the user.
	if looksLikeJSONL(rawOut) {
		if ctx.Err() == context.DeadlineExceeded {
			return "codex command timeout"
		}
		// Try to extract a structured error from the JSONL events.
		if errMsg := extractErrorFromJSONL(rawOut); errMsg != "" {
			return errMsg
		}
		if execErr != nil {
			return "codex failed: " + formatExecError(execErr)
		}
		return "Sorry, I was unable to generate a response for this request."
	}

	// Fall back to raw stdout/stderr.
	result := strings.TrimSpace(rawOut)
	if ctx.Err() == context.DeadlineExceeded {
		return "codex command timeout"
	}
	if execErr != nil {
		if result == "" {
			return "codex failed: " + formatExecError(execErr)
		}
		// Try to extract a human-readable error message from the JSONL
		// output. Codex emits both {"type":"error","message":"..."} and
		// {"type":"turn.failed","error":{"message":"..."}} with the same
		// text; without dedup the user sees the error twice.
		if errMsg := extractErrorFromJSONL(result); errMsg != "" {
			return errMsg
		}
		return result
	}
	if result == "" {
		return "(empty response)"
	}
	return result
}

// extractErrorFromJSONL scans JSONL output for a codex error event and returns
// the deduplicated error message. Returns "" if no error event is found.
// looksLikeJSONL returns true if the output starts with valid JSON lines
// (indicating codex ran in --json mode but may have produced no text).
func looksLikeJSONL(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func extractErrorFromJSONL(raw string) string {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev jsonEvent
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		// Prefer the top-level "error" event message.
		if ev.Type == "error" && ev.Message != "" {
			return ev.Message
		}
		// Fall back to turn.failed error payload.
		if ev.Type == "turn.failed" && ev.Error != nil && ev.Error.Message != "" {
			return ev.Error.Message
		}
	}
	return ""
}

// resumeSandboxArgs returns CLI flags for `codex exec resume` that replicate
// the given sandbox level. `resume` does not support `--sandbox`, so we
// map to the equivalent flags it does accept.
func resumeSandboxArgs(sandbox string) []string {
	switch sandbox {
	case "danger-full-access":
		return []string{"--dangerously-bypass-approvals-and-sandbox"}
	case "workspace-write":
		return []string{"--full-auto"}
	default:
		// "read-only" or empty: codex default, no extra flags needed.
		return nil
	}
}
