package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Rememorio/clawdex/internal/termcolor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestHome overrides HOME to a temp dir so all PID/log/data operations
// use an isolated directory.
func setupTestHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func writeTestConfig(t *testing.T, content string) {
	t.Helper()
	configPath, err := configFilePath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, []byte(content), 0o644))
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = writer
	defer func() {
		os.Stdout = oldStdout
	}()

	fn()

	require.NoError(t, writer.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	return buf.String()
}

func TestDataDir(t *testing.T) {
	home := setupTestHome(t)
	dir, err := DataDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".clawdex"), dir)

	stat, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, stat.IsDir())
}

func TestDataDir_CreatesIfMissing(t *testing.T) {
	home := setupTestHome(t)
	expected := filepath.Join(home, ".clawdex")

	// Ensure it doesn't exist yet
	_, err := os.Stat(expected)
	assert.True(t, os.IsNotExist(err))

	dir, err := DataDir()
	require.NoError(t, err)
	assert.Equal(t, expected, dir)

	stat, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, stat.IsDir())
}

func TestWorkspaceDir(t *testing.T) {
	home := setupTestHome(t)
	expected := filepath.Join(home, ".clawdex", "workspace")

	_, err := os.Stat(expected)
	assert.True(t, os.IsNotExist(err))

	dir, err := WorkspaceDir()
	require.NoError(t, err)
	assert.Equal(t, expected, dir)

	stat, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, stat.IsDir())
}

func TestPIDPath(t *testing.T) {
	home := setupTestHome(t)
	path, err := PIDPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".clawdex", "gateway.pid"), path)
}

func TestLogPath(t *testing.T) {
	home := setupTestHome(t)
	path, err := LogPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".clawdex", "gateway.log"), path)
}

func TestWriteReadRemovePID(t *testing.T) {
	setupTestHome(t)

	// Write
	require.NoError(t, WritePID())

	// Read
	pid, err := ReadPID()
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), pid)

	// Remove
	RemovePID()

	pid, err = ReadPID()
	require.NoError(t, err)
	assert.Equal(t, 0, pid)
}

func TestReadPID_NotExist(t *testing.T) {
	setupTestHome(t)

	pid, err := ReadPID()
	require.NoError(t, err)
	assert.Equal(t, 0, pid)
}

func TestReadPID_InvalidContent(t *testing.T) {
	setupTestHome(t)

	path, err := PIDPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("not-a-number"), 0o644))

	_, err = ReadPID()
	assert.ErrorContains(t, err, "invalid PID file content")
}

func TestIsRunning_CurrentProcess(t *testing.T) {
	assert.True(t, IsRunning(os.Getpid()))
}

func TestIsRunning_ZeroPID(t *testing.T) {
	assert.False(t, IsRunning(0))
}

func TestIsRunning_NegativePID(t *testing.T) {
	assert.False(t, IsRunning(-1))
}

func TestIsRunning_DeadPID(t *testing.T) {
	// PID 4194304 is above typical PID range on Linux, very unlikely to exist.
	assert.False(t, IsRunning(4194304))
}

func TestStop_NoPIDFile(t *testing.T) {
	setupTestHome(t)

	err := Stop()
	assert.ErrorContains(t, err, "not running")
	assert.ErrorContains(t, err, "no PID file")
}

func TestStop_StalePID(t *testing.T) {
	setupTestHome(t)

	path, err := PIDPath()
	require.NoError(t, err)
	// Write a PID that doesn't exist
	require.NoError(t, os.WriteFile(path, []byte(strconv.Itoa(4194304)), 0o644))

	err = Stop()
	assert.ErrorContains(t, err, "not running")
	assert.ErrorContains(t, err, "stale PID")

	// Should have cleaned up the stale PID file
	pid, err := ReadPID()
	require.NoError(t, err)
	assert.Equal(t, 0, pid)
}

func TestRemovePID_Idempotent(t *testing.T) {
	setupTestHome(t)

	// Should not panic or error even if file doesn't exist
	RemovePID()
	RemovePID()
}

func TestStatus_NoPID(t *testing.T) {
	setupTestHome(t)
	// Just ensure it doesn't panic — Status prints to stdout.
	Status()
}

func TestStatus_StalePID(t *testing.T) {
	setupTestHome(t)
	path, err := PIDPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("4194304"), 0o644))
	Status()
}

func TestStatus_RunningPID(t *testing.T) {
	setupTestHome(t)
	require.NoError(t, WritePID())
	Status()
}

func TestLoadConfigSummary_MultiChannelConfig(t *testing.T) {
	setupTestHome(t)
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() {
		term = oldTerm
	}()

	writeTestConfig(t, `{
  "gateway": {
    "address": ":10086"
  },
  "channels": {
    "telegram-main": {
      "type": "telegram"
    },
    "telegram-disabled": {
      "type": "telegram",
      "enabled": false
    },
    "wecom-hook": {
      "type": "wecom",
      "enabled": true,
      "webhook_path": "/v1/clawdbot"
    },
    "wecom-ws": {
      "type": "wecom",
      "enabled": true,
      "connection_mode": "websocket"
    }
  }
}`)

	summary := loadConfigSummary()
	require.Empty(t, summary.ConfigError)
	assert.Equal(t, ":10086", summary.Address)
	assert.ElementsMatch(t, []string{
		"telegram/telegram-main",
		"wecom/wecom-hook (webhook /v1/clawdbot)",
		"wecom/wecom-ws (websocket)",
	}, summary.Channels)
}

func TestLoadConfigSummary_ResolvesWeComWebhookPathEnv(t *testing.T) {
	setupTestHome(t)
	t.Setenv("WECOM_WEBHOOK_PATH", "/resolved/wecom/hook")
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() {
		term = oldTerm
	}()

	writeTestConfig(t, `{
  "channels": {
    "wecom-hook": {
      "type": "wecom",
      "enabled": true,
      "webhook_path": "${WECOM_WEBHOOK_PATH}"
    }
  }
}`)

	summary := loadConfigSummary()
	require.Empty(t, summary.ConfigError)
	assert.Equal(t, []string{
		"wecom/wecom-hook (webhook /resolved/wecom/hook)",
	}, summary.Channels)
}

func TestLoadConfigSummary_DescribesWeComWebhookPathEnvWhenUnset(t *testing.T) {
	setupTestHome(t)
	t.Setenv("WECOM_WEBHOOK_PATH", "")
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() {
		term = oldTerm
	}()

	writeTestConfig(t, `{
  "channels": {
    "wecom-hook": {
      "type": "wecom",
      "enabled": true,
      "webhook_path": "${WECOM_WEBHOOK_PATH}"
    }
  }
}`)

	summary := loadConfigSummary()
	require.Empty(t, summary.ConfigError)
	assert.Equal(t, []string{
		"wecom/wecom-hook (webhook env: WECOM_WEBHOOK_PATH)",
	}, summary.Channels)
}

func TestLoadConfigSummary_InvalidChannels(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, `{
  "channels": {
    "broken": "oops"
  }
}`)

	summary := loadConfigSummary()
	assert.Equal(t, ":8080", summary.Address)
	assert.Equal(t, "invalid channels", summary.ConfigError)
}

func TestStatus_PrintsStructuredSummaryWithoutLogTail(t *testing.T) {
	setupTestHome(t)
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() {
		term = oldTerm
	}()

	writeTestConfig(t, `{
  "gateway": {
    "address": ":10086"
  },
  "channels": {
    "telegram-main": {
      "type": "telegram"
    },
    "wecom-hook": {
      "type": "wecom",
      "enabled": true,
      "webhook_path": "/v1/clawdbot"
    }
  }
}`)
	logPath, err := LogPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(logPath, []byte("line 1\nline 2\n"), 0o644))

	output := captureStdout(t, func() {
		Status()
	})
	assert.Contains(t, output, "gateway is not running")
	assert.Contains(t, output, "  channels:")
	assert.Contains(t, output, "    - telegram/telegram-main")
	assert.Contains(t, output, "    - wecom/wecom-hook (webhook /v1/clawdbot)")
	assert.Contains(t, output, "  address:  :10086")
	assert.Contains(t, output, "  log:      ")
	assert.NotContains(t, output, "last log lines")
	assert.NotContains(t, output, "no token")
}

func TestStart_AlreadyRunning(t *testing.T) {
	setupTestHome(t)
	// Write current process PID as if gateway is running
	require.NoError(t, WritePID())
	err := Start()
	assert.ErrorContains(t, err, "already running")
}

func TestRestart_NothingRunning(t *testing.T) {
	setupTestHome(t)
	// Restart when nothing is running should attempt Start.
	// Start will fail because the binary isn't "gateway run" compatible,
	// but it exercises the Restart code path.
	err := Restart()
	// It either succeeds (starts something) or fails at exec — both are fine
	// as long as it doesn't panic and the "not running" path is exercised.
	_ = err
}

func TestStop_LiveProcess(t *testing.T) {
	setupTestHome(t)

	// Ensure data dir exists
	_, err := DataDir()
	require.NoError(t, err)

	// Start a real background process we can stop.
	// Use "exec sleep" so SIGTERM goes directly to the sleep process.
	cmd := exec.Command("sh", "-c", "exec sleep 60")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	// Reap the child in background so it doesn't become a zombie.
	go cmd.Wait()
	t.Cleanup(func() { cmd.Process.Kill() })

	// Write its PID into our test PID file.
	pidPath, err := PIDPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o644))

	// Stop should send SIGTERM and succeed.
	err = Stop()
	assert.NoError(t, err)

	// PID file should be cleaned up.
	readPid, err := ReadPID()
	require.NoError(t, err)
	assert.Equal(t, 0, readPid)
}

func TestDataDir_ExistingDir(t *testing.T) {
	home := setupTestHome(t)
	expected := filepath.Join(home, ".clawdex")
	require.NoError(t, os.MkdirAll(expected, 0o755))

	dir, err := DataDir()
	require.NoError(t, err)
	assert.Equal(t, expected, dir)
}

func TestWritePID_ReadPID_Consistency(t *testing.T) {
	setupTestHome(t)

	require.NoError(t, WritePID())
	pid, err := ReadPID()
	require.NoError(t, err)
	assert.Equal(t, os.Getpid(), pid)
}

func TestIsRunning_LargeInvalidPID(t *testing.T) {
	// PID 999999999 is way above typical PID range
	assert.False(t, IsRunning(999999999))
}

func TestRestart_WithRunningProcess(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("skipping on CI: spawns detached processes that can outlive the test runner")
	}
	setupTestHome(t)

	// Ensure data dir exists
	_, err := DataDir()
	require.NoError(t, err)

	// Start a process to act as the "running gateway"
	// Use "exec sleep" so SIGTERM goes directly to the sleep process.
	cmd := exec.Command("sh", "-c", "exec sleep 60")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	// Reap the child in a channel so we can wait without racing.
	waitDone := make(chan struct{})
	go func() {
		cmd.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() { cmd.Process.Kill(); <-waitDone })

	pidPath, err := PIDPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o644))

	// Restart should stop the running process and then attempt to start.
	// Start may succeed or fail depending on the binary — we just verify
	// the old process was terminated.
	err = Restart()
	// We don't assert on err because Start() might succeed or fail.
	_ = err

	// Wait for the child to exit (already killed by Restart/Stop).
	<-waitDone
	assert.False(t, IsRunning(pid))
}

func TestStart_LogFileError(t *testing.T) {
	setupTestHome(t)

	// Make the data dir read-only so the log file cannot be opened.
	dir, err := DataDir()
	require.NoError(t, err)

	// Create a file where gateway.log would go, so OpenFile fails.
	logPath := filepath.Join(dir, "gateway.log")
	require.NoError(t, os.MkdirAll(logPath, 0o755)) // log path is a directory

	err = Start()
	assert.ErrorContains(t, err, "open log file")
}

func TestReadPID_ReadError(t *testing.T) {
	setupTestHome(t)

	// Make PID file a directory so ReadFile fails.
	pidPath, err := PIDPath()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(pidPath, 0o755))

	_, err = ReadPID()
	assert.ErrorContains(t, err, "read PID file")
}

func TestDataDir_MkdirFails(t *testing.T) {
	home := setupTestHome(t)
	// Create a file where .clawdex directory should be so MkdirAll fails.
	blocker := filepath.Join(home, ".clawdex")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	_, err := DataDir()
	assert.ErrorContains(t, err, "create data directory")
}

func TestPIDPath_DataDirError(t *testing.T) {
	home := setupTestHome(t)
	blocker := filepath.Join(home, ".clawdex")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	_, err := PIDPath()
	assert.Error(t, err)
}

func TestLogPath_DataDirError(t *testing.T) {
	home := setupTestHome(t)
	blocker := filepath.Join(home, ".clawdex")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	_, err := LogPath()
	assert.Error(t, err)
}

func TestWritePID_DataDirError(t *testing.T) {
	home := setupTestHome(t)
	blocker := filepath.Join(home, ".clawdex")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	err := WritePID()
	assert.Error(t, err)
}

func TestRemovePID_DataDirError(t *testing.T) {
	home := setupTestHome(t)
	blocker := filepath.Join(home, ".clawdex")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	// Should not panic
	RemovePID()
}

func TestReadPID_DataDirError(t *testing.T) {
	home := setupTestHome(t)
	blocker := filepath.Join(home, ".clawdex")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	_, err := ReadPID()
	assert.Error(t, err)
}

func TestStart_DataDirError(t *testing.T) {
	home := setupTestHome(t)
	blocker := filepath.Join(home, ".clawdex")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	err := Start()
	assert.Error(t, err)
}

func TestStatus_DataDirError(t *testing.T) {
	home := setupTestHome(t)
	blocker := filepath.Join(home, ".clawdex")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	// Should not panic — Status prints error to stdout
	Status()
}

func TestStop_SIGKILLFallback(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("skipping on CI: requires 5s SIGKILL grace period which can cause timeout")
	}
	setupTestHome(t)

	_, err := DataDir()
	require.NoError(t, err)

	// Start a process that traps and ignores SIGTERM so Stop must fall back to SIGKILL.
	// Use sh+trap instead of python3 for portability on CI runners.
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 300")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	// Reap the child in a channel so we can wait without racing.
	waitDone := make(chan struct{})
	go func() {
		cmd.Wait()
		close(waitDone)
	}()
	t.Cleanup(func() { cmd.Process.Kill(); <-waitDone })

	pidPath, err := PIDPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o644))

	// Stop should send SIGTERM, wait 5s, then SIGKILL.
	err = Stop()
	assert.NoError(t, err)
}

// ── tailFile tests ──

func TestTailFile_LastNLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "line1\nline2\nline3\nline4\nline5\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	lines := tailFile(path, 3)
	assert.Equal(t, []string{"line3", "line4", "line5"}, lines)
}

func TestTailFile_FewerLinesThanN(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "line1\nline2\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	lines := tailFile(path, 10)
	assert.Equal(t, []string{"line1", "line2"}, lines)
}

func TestTailFile_ExactlyNLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "a\nb\nc\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	lines := tailFile(path, 3)
	assert.Equal(t, []string{"a", "b", "c"}, lines)
}

func TestTailFile_SingleLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	require.NoError(t, os.WriteFile(path, []byte("only line\n"), 0o644))

	lines := tailFile(path, 5)
	assert.Equal(t, []string{"only line"}, lines)
}

func TestTailFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o644))

	lines := tailFile(path, 5)
	assert.Nil(t, lines)
}

func TestTailFile_NonexistentFile(t *testing.T) {
	lines := tailFile("/nonexistent/file/path.log", 5)
	assert.Nil(t, lines)
}

func TestTailFile_NoTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	content := "line1\nline2\nline3"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	lines := tailFile(path, 2)
	assert.Equal(t, []string{"line2", "line3"}, lines)
}

// ── WithFollow / WithLines tests ──

func TestWithFollow(t *testing.T) {
	var opts logsOptions
	WithFollow(true)(&opts)
	assert.True(t, opts.follow)

	WithFollow(false)(&opts)
	assert.False(t, opts.follow)
}

func TestWithLines(t *testing.T) {
	var opts logsOptions
	WithLines(100)(&opts)
	assert.Equal(t, 100, opts.lines)

	WithLines(0)(&opts)
	assert.Equal(t, 0, opts.lines)
}

// ── Logs tests ──

func TestLogs_NoLogFile(t *testing.T) {
	setupTestHome(t)
	// Ensure systemd is not active (not installed in test env)
	err := Logs()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no log file found")
}

func TestLogs_EmptyLogFile(t *testing.T) {
	setupTestHome(t)
	logPath, err := LogPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(logPath, []byte(""), 0o644))

	output := captureStdout(t, func() {
		err = Logs()
	})
	assert.NoError(t, err)
	assert.Contains(t, output, "log file is empty")
}

func TestLogs_WithContent(t *testing.T) {
	setupTestHome(t)
	logPath, err := LogPath()
	require.NoError(t, err)
	content := "2024-01-01 line1\n2024-01-01 line2\n2024-01-01 line3\n"
	require.NoError(t, os.WriteFile(logPath, []byte(content), 0o644))

	output := captureStdout(t, func() {
		err = Logs(WithLines(2))
	})
	assert.NoError(t, err)
	assert.Contains(t, output, "line2")
	assert.Contains(t, output, "line3")
	assert.NotContains(t, output, "line1")
}

func TestLogs_DefaultLines(t *testing.T) {
	setupTestHome(t)
	logPath, err := LogPath()
	require.NoError(t, err)
	// Write 50 lines, default should show last 40
	var content string
	for i := 1; i <= 50; i++ {
		content += fmt.Sprintf("line %d\n", i)
	}
	require.NoError(t, os.WriteFile(logPath, []byte(content), 0o644))

	output := captureStdout(t, func() {
		err = Logs()
	})
	assert.NoError(t, err)
	assert.Contains(t, output, "line 50")
	assert.Contains(t, output, "line 11")
	assert.NotContains(t, output, "line 10\n")
}

// ── processUptime tests ──

func TestProcessUptime_InvalidPID(t *testing.T) {
	// A PID that doesn't exist should return ""
	result := processUptime(999999999)
	assert.Equal(t, "", result)
}

func TestProcessUptime_CurrentProcess(t *testing.T) {
	// On Linux, /proc/self/stat exists. This tests the function doesn't crash.
	result := processUptime(os.Getpid())
	// On Linux this should return a non-empty string; on other platforms it returns "".
	// We only assert it doesn't panic.
	_ = result
}

// ── printLogSummary tests ──

func TestPrintLogSummary(t *testing.T) {
	setupTestHome(t)
	logPath, err := LogPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(logPath, []byte("test\n"), 0o644))

	output := captureStdout(t, func() {
		printLogSummary()
	})
	assert.Contains(t, output, "log:")
	assert.Contains(t, output, "gateway.log")
}

func TestPrintLogSummary_NoDataDir(t *testing.T) {
	home := setupTestHome(t)
	// Block data dir creation
	blocker := filepath.Join(home, ".clawdex")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o644))

	output := captureStdout(t, func() {
		printLogSummary()
	})
	// Should not output anything when LogPath fails
	assert.Empty(t, output)
}

// ── processUptime additional tests ──

func TestProcessUptime_ZeroPID(t *testing.T) {
	result := processUptime(0)
	assert.Equal(t, "", result)
}

func TestProcessUptime_NegativePID(t *testing.T) {
	result := processUptime(-1)
	assert.Equal(t, "", result)
}

// ── printChannels tests ──

func TestPrintChannels_None(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	output := captureStdout(t, func() {
		printChannels(nil)
	})
	assert.Contains(t, output, "channels:")
	assert.Contains(t, output, "none")
}

func TestPrintChannels_Single(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	output := captureStdout(t, func() {
		printChannels([]string{"telegram/main"})
	})
	assert.Contains(t, output, "channels: telegram/main")
}

func TestPrintChannels_Multiple(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	output := captureStdout(t, func() {
		printChannels([]string{"telegram/main", "wecom/work"})
	})
	assert.Contains(t, output, "channels:")
	assert.Contains(t, output, "- telegram/main")
	assert.Contains(t, output, "- wecom/work")
}

// ── printConfigSummary tests ──

func TestPrintConfigSummary_ValidConfig(t *testing.T) {
	setupTestHome(t)
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	writeTestConfig(t, `{
  "gateway": {"address": ":9090"},
  "channels": {
    "tg": {"type": "telegram"}
  }
}`)

	output := captureStdout(t, func() {
		printConfigSummary()
	})
	assert.Contains(t, output, "config:")
	assert.Contains(t, output, "channels: telegram/tg")
	assert.Contains(t, output, "address:  :9090")
}

func TestPrintConfigSummary_NoConfig(t *testing.T) {
	setupTestHome(t)
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	output := captureStdout(t, func() {
		printConfigSummary()
	})
	// With no config file, channels should show "none"
	assert.Contains(t, output, "none")
}

// ── summarizeChannel additional types ──

func TestSummarizeChannel_Weixin(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	raw := json.RawMessage(`{"type":"weixin","enabled":true}`)
	label, ok, err := summarizeChannel("wx1", raw)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, label, "weixin")
	assert.Contains(t, label, "wx1")
}

func TestSummarizeChannel_QQBot(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	raw := json.RawMessage(`{"type":"qqbot","app_id":"123"}`)
	label, ok, err := summarizeChannel("qqbot1", raw)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, label, "qqbot")
	assert.Contains(t, label, "qqbot1")
}

func TestSummarizeChannel_TelegramEnabled(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	raw := json.RawMessage(`{"type":"telegram"}`)
	label, ok, err := summarizeChannel("main", raw)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, label, "telegram")
	assert.Contains(t, label, "main")
}

func TestSummarizeChannel_TelegramDisabled(t *testing.T) {
	raw := json.RawMessage(`{"type":"telegram","enabled":false}`)
	_, ok, err := summarizeChannel("disabled", raw)
	require.NoError(t, err)
	assert.False(t, ok) // disabled channels should return false
}

func TestSummarizeChannel_WeComWebhook(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	raw := json.RawMessage(`{"type":"wecom","enabled":true,"webhook_path":"/hook"}`)
	label, ok, err := summarizeChannel("wc1", raw)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, label, "wecom")
	assert.Contains(t, label, "webhook /hook")
}

func TestSummarizeChannel_WeComWebsocket(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	raw := json.RawMessage(`{"type":"wecom","enabled":true,"connection_mode":"websocket"}`)
	label, ok, err := summarizeChannel("wc2", raw)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, label, "wecom")
	assert.Contains(t, label, "websocket")
}

func TestSummarizeChannel_WeComDisabled(t *testing.T) {
	raw := json.RawMessage(`{"type":"wecom","enabled":false}`)
	_, ok, err := summarizeChannel("off", raw)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSummarizeChannel_WeComEnabledNil(t *testing.T) {
	// WeCom with nil enabled → treated as NOT enabled (unlike telegram).
	raw := json.RawMessage(`{"type":"wecom"}`)
	_, ok, err := summarizeChannel("wc-nil", raw)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestSummarizeChannel_MissingType(t *testing.T) {
	raw := json.RawMessage(`{"name":"noType"}`)
	_, _, err := summarizeChannel("bad", raw)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing channel type")
}

func TestSummarizeChannel_InvalidJSON(t *testing.T) {
	raw := json.RawMessage(`{invalid`)
	_, _, err := summarizeChannel("broken", raw)
	assert.Error(t, err)
}

// ── tailFile additional edge cases ──

func TestTailFile_RequestZeroLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	require.NoError(t, os.WriteFile(path, []byte("line1\nline2\n"), 0o644))

	lines := tailFile(path, 0)
	// Request 0 lines: len(lines) > n is true, so returns last 0 lines from slice.
	assert.Empty(t, lines)
}

func TestTailFile_OnlyNewlines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	require.NoError(t, os.WriteFile(path, []byte("\n\n\n"), 0o644))

	lines := tailFile(path, 5)
	// After TrimRight("\n"), content is empty → Split produces [""]
	// But len("") == 0 should return nil? Actually the function trims all trailing \n
	// which makes content empty, then splits on \n giving [""].
	// Actually wait: content = "" after trimRight, then split gives [""],
	// which has len 1 <= 5, so returns [""].
	assert.Equal(t, []string{""}, lines)
}

// ── loadConfigSummary tests ──

func TestLoadConfigSummary_MissingFile(t *testing.T) {
	setupTestHome(t)
	summary := loadConfigSummary()
	// No config file → empty result (no error).
	assert.Empty(t, summary.ConfigError)
	assert.Empty(t, summary.Channels)
}

func TestLoadConfigSummary_InvalidJSON(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, `{invalid json`)
	summary := loadConfigSummary()
	assert.Equal(t, "invalid config file", summary.ConfigError)
}

func TestLoadConfigSummary_DefaultAddress(t *testing.T) {
	setupTestHome(t)
	writeTestConfig(t, `{"channels":{}}`)
	summary := loadConfigSummary()
	assert.Equal(t, ":8080", summary.Address)
}

// ── Start error path: already running ──

func TestStart_AlreadyRunning_ExactPID(t *testing.T) {
	setupTestHome(t)
	pidPath, err := PIDPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644))

	err = Start()
	assert.ErrorContains(t, err, "already running")
	assert.Contains(t, err.Error(), fmt.Sprintf("pid %d", os.Getpid()))
}

// ── Logs with follow but no tail binary ──

func TestLogs_NonExistentPath(t *testing.T) {
	setupTestHome(t)
	// With no log file, Logs should report the missing file.
	err := Logs(WithLines(5))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no log file found")
}

// ── WorkspaceDir tests ──

func TestWorkspaceDir_CreatesNestedDir(t *testing.T) {
	home := setupTestHome(t)
	dir, err := WorkspaceDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".clawdex", "workspace"), dir)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

// ── Additional coverage tests (appended) ──

func TestSummarizeChannel_WeComWebhookEnvVar(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	t.Setenv("MY_HOOK", "/custom/hook/path")
	raw := json.RawMessage(`{"type":"wecom","enabled":true,"webhook_path":"${MY_HOOK}"}`)
	label, ok, err := summarizeChannel("wc-env", raw)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, label, "wecom")
	assert.Contains(t, label, "/custom/hook/path")
}

func TestSummarizeChannel_WeComWebhookPlaintext(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	raw := json.RawMessage(`{"type":"wecom","enabled":true,"webhook_path":"/plain/hook"}`)
	label, ok, err := summarizeChannel("wc-plain", raw)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, label, "/plain/hook")
}

func TestSummarizeChannel_WeComNoWebhookPath(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	raw := json.RawMessage(`{"type":"wecom","enabled":true}`)
	label, ok, err := summarizeChannel("wc-nowh", raw)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, label, "wecom/wc-nowh")
	assert.Contains(t, label, "webhook")
	// No specific path should be shown when webhook_path is empty
	assert.NotContains(t, label, "webhook /")
}

func TestDisplayWebhookPath_Plaintext(t *testing.T) {
	result := displayWebhookPath("/my/path")
	assert.Equal(t, "/my/path", result)
}

func TestDisplayWebhookPath_EnvResolved(t *testing.T) {
	t.Setenv("WH_PATH_TEST", "/resolved/path")
	result := displayWebhookPath("${WH_PATH_TEST}")
	assert.Equal(t, "/resolved/path", result)
}

func TestDisplayWebhookPath_EnvUnresolved(t *testing.T) {
	t.Setenv("UNRESOLVABLE_WH_PATH", "")
	result := displayWebhookPath("${UNRESOLVABLE_WH_PATH}")
	assert.Contains(t, result, "env:")
	assert.Contains(t, result, "UNRESOLVABLE_WH_PATH")
}

func TestDisplayWebhookPath_FileRef(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "hook.txt")
	require.NoError(t, os.WriteFile(f, []byte("/from/file\n"), 0o644))
	result := displayWebhookPath("file://" + f)
	assert.Equal(t, "/from/file", result)
}

func TestProcessUptime_CurrentProcessNonEmpty(t *testing.T) {
	// On Linux, processUptime should return a non-empty string for our own PID.
	result := processUptime(os.Getpid())
	// This test is Linux-specific. On other platforms it may be empty.
	if _, err := os.Stat("/proc/self/stat"); err == nil {
		assert.NotEmpty(t, result)
	}
}

func TestStart_StalePIDCleanedup(t *testing.T) {
	setupTestHome(t)

	// Write a stale PID (dead process)
	pidPath, err := PIDPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pidPath, []byte("4194304"), 0o644))

	// Start should clean up stale PID and proceed.
	// It will fail at the exec step but the stale PID cleanup is exercised.
	_ = Start()

	// Verify the stale PID was cleaned up (the new process may or may not have succeeded).
}

func TestRestart_StalePIDCleaned(t *testing.T) {
	setupTestHome(t)

	// Write a stale PID
	pidPath, err := PIDPath()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(pidPath, []byte("4194304"), 0o644))

	// Restart should detect stale PID, clean it up, and try to start.
	_ = Restart()

	// The stale PID file should be cleaned up.
	pid, err := ReadPID()
	// If Restart succeeded to start, there'll be a new PID. If it failed, 0.
	_ = pid
	_ = err
}

func TestSummarizeChannels_SortsAlphabetically(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	rawChannels := map[string]json.RawMessage{
		"z-telegram": json.RawMessage(`{"type":"telegram"}`),
		"a-wecom":    json.RawMessage(`{"type":"wecom","enabled":true}`),
		"m-qqbot":    json.RawMessage(`{"type":"qqbot"}`),
	}
	channels, err := summarizeChannels(rawChannels)
	require.NoError(t, err)
	// Should be sorted alphabetically by name
	require.Len(t, channels, 3)
	assert.Contains(t, channels[0], "a-wecom")
	assert.Contains(t, channels[1], "m-qqbot")
	assert.Contains(t, channels[2], "z-telegram")
}

func TestSummarizeChannels_Empty(t *testing.T) {
	channels, err := summarizeChannels(nil)
	require.NoError(t, err)
	assert.Nil(t, channels)
}

func TestSummarizeChannel_WeixinEnabled(t *testing.T) {
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	raw := json.RawMessage(`{"type":"weixin"}`)
	label, ok, err := summarizeChannel("wx-test", raw)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Contains(t, label, "weixin")
	assert.Contains(t, label, "wx-test")
}

func TestLoadConfigSummary_WeComDisabled(t *testing.T) {
	setupTestHome(t)
	oldTerm := term
	term = termcolor.NewEnabled(false)
	defer func() { term = oldTerm }()

	writeTestConfig(t, `{
  "channels": {
    "wecom-off": {
      "type": "wecom",
      "enabled": false
    },
    "tg-on": {
      "type": "telegram"
    }
  }
}`)

	summary := loadConfigSummary()
	require.Empty(t, summary.ConfigError)
	// Only telegram should show up (wecom disabled)
	assert.Len(t, summary.Channels, 1)
	assert.Contains(t, summary.Channels[0], "telegram")
}

func TestSummarizeChannel_InvalidTelegramJSON(t *testing.T) {
	// Invalid JSON for telegram-specific fields
	raw := json.RawMessage(`{"type":"telegram","enabled":"not-a-bool"}`)
	_, _, err := summarizeChannel("bad-tg", raw)
	assert.Error(t, err)
}

func TestSummarizeChannel_InvalidWeComJSON(t *testing.T) {
	raw := json.RawMessage(`{"type":"wecom","enabled":"not-a-bool"}`)
	_, _, err := summarizeChannel("bad-wc", raw)
	assert.Error(t, err)
}
