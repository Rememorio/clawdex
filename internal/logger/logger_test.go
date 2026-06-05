package logger

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestSetLevel(t *testing.T) {
	tests := []struct {
		name     string
		level    Level
		expected Level
	}{
		{"debug level", LevelDebug, LevelDebug},
		{"info level", LevelInfo, LevelInfo},
		{"warn level", LevelWarn, LevelWarn},
		{"error level", LevelError, LevelError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetLevel(tt.level)
			if defaultLevel != tt.expected {
				t.Errorf("SetLevel() level = %v, want %v", defaultLevel, tt.expected)
			}
		})
	}
}

func TestSetOutput(t *testing.T) {
	var buf bytes.Buffer
	SetLevel(LevelInfo)
	SetOutput(&buf)

	Info("test message")

	output := buf.String()
	if !strings.Contains(output, "test message") {
		t.Errorf("SetOutput() output = %q, want to contain %q", output, "test message")
	}
}

func TestLogLevels(t *testing.T) {
	tests := []struct {
		name      string
		logFunc   func(string, ...any)
		level     Level
		msg       string
		shouldLog bool
	}{
		{"debug at debug level", Debug, LevelDebug, "debug msg", true},
		{"info at info level", Info, LevelInfo, "info msg", true},
		{"warn at warn level", Warn, LevelWarn, "warn msg", true},
		{"error at error level", Error, LevelError, "error msg", true},
		{"debug at info level", Debug, LevelInfo, "debug msg", false},
		{"info at warn level", Info, LevelWarn, "info msg", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			SetLevel(tt.level)
			SetOutput(&buf)

			tt.logFunc(tt.msg)

			output := buf.String()
			if tt.shouldLog && !strings.Contains(output, tt.msg) {
				t.Errorf("expected log to contain %q, got %q", tt.msg, output)
			}
			if !tt.shouldLog && output != "" {
				t.Errorf("expected no log output, got %q", output)
			}
		})
	}
}

func TestLogWithArgs(t *testing.T) {
	var buf bytes.Buffer
	SetLevel(LevelDebug)
	SetOutput(&buf)

	Info("test message", "key", "value", "count", 42)

	output := buf.String()
	if !strings.Contains(output, "test message") {
		t.Errorf("expected log to contain %q, got %q", "test message", output)
	}
	if !strings.Contains(output, "key=value") {
		t.Errorf("expected log to contain %q, got %q", "key=value", output)
	}
	if !strings.Contains(output, "count=42") {
		t.Errorf("expected log to contain %q, got %q", "count=42", output)
	}
}

func TestContextFunctions(t *testing.T) {
	tests := []struct {
		name    string
		logFunc func(context.Context, string, ...any)
		level   Level
	}{
		{"DebugContext", DebugContext, LevelDebug},
		{"InfoContext", InfoContext, LevelInfo},
		{"WarnContext", WarnContext, LevelWarn},
		{"ErrorContext", ErrorContext, LevelError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			SetLevel(tt.level)
			SetOutput(&buf)

			ctx := context.Background()
			tt.logFunc(ctx, "context message", "key", "value")

			output := buf.String()
			if !strings.Contains(output, "context message") {
				t.Errorf("expected log to contain %q, got %q", "context message", output)
			}
		})
	}
}

func TestWith(t *testing.T) {
	var buf bytes.Buffer
	SetLevel(LevelInfo)
	SetOutput(&buf)

	logger := With("service", "test")
	logger.Info("with message")

	output := buf.String()
	if !strings.Contains(output, "with message") {
		t.Errorf("expected log to contain %q, got %q", "with message", output)
	}
	if !strings.Contains(output, "service=test") {
		t.Errorf("expected log to contain %q, got %q", "service=test", output)
	}
}

func TestL(t *testing.T) {
	logger := L()
	if logger == nil {
		t.Error("L() returned nil logger")
	}
}

func TestLevelConstants(t *testing.T) {
	if LevelDebug != slog.LevelDebug {
		t.Errorf("LevelDebug = %v, want %v", LevelDebug, slog.LevelDebug)
	}
	if LevelInfo != slog.LevelInfo {
		t.Errorf("LevelInfo = %v, want %v", LevelInfo, slog.LevelInfo)
	}
	if LevelWarn != slog.LevelWarn {
		t.Errorf("LevelWarn = %v, want %v", LevelWarn, slog.LevelWarn)
	}
	if LevelError != slog.LevelError {
		t.Errorf("LevelError = %v, want %v", LevelError, slog.LevelError)
	}
}

func TestConfigure_DebugJSON(t *testing.T) {
	var buf bytes.Buffer
	Configure(Config{Level: "debug", Format: "json"})
	SetOutput(&buf)

	Debug("test json debug")

	output := buf.String()
	if !strings.Contains(output, "test json debug") {
		t.Errorf("expected output to contain %q, got %q", "test json debug", output)
	}
	// JSON format should produce valid JSON-like output with msg field.
	if !strings.Contains(output, `"msg"`) {
		t.Errorf("expected JSON format with msg field, got %q", output)
	}
}

func TestConfigure_WarnText(t *testing.T) {
	var buf bytes.Buffer
	Configure(Config{Level: "warn", Format: "text"})
	SetOutput(&buf)

	Info("should not appear")
	output := buf.String()
	if output != "" {
		t.Errorf("expected no output for info at warn level, got %q", output)
	}

	Warn("should appear")
	output = buf.String()
	if !strings.Contains(output, "should appear") {
		t.Errorf("expected output to contain %q, got %q", "should appear", output)
	}
}

func TestConfigure_EmptyFormat(t *testing.T) {
	var buf bytes.Buffer
	Configure(Config{Level: "error", Format: ""})
	SetOutput(&buf)

	Error("error msg")
	output := buf.String()
	if !strings.Contains(output, "error msg") {
		t.Errorf("expected output to contain %q, got %q", "error msg", output)
	}
	// Empty format should default to text (no JSON "msg" field).
	if strings.Contains(output, `"msg"`) {
		t.Errorf("expected text format (default), got JSON-like output %q", output)
	}
}

func TestConfigure_UnknownLevel(t *testing.T) {
	var buf bytes.Buffer
	// Unknown level should default to info.
	Configure(Config{Level: "unknown"})
	SetOutput(&buf)

	Info("info msg")
	output := buf.String()
	if !strings.Contains(output, "info msg") {
		t.Errorf("expected output to contain %q, got %q", "info msg", output)
	}
}

func TestParseLevel(t *testing.T) {
	tests := []struct {
		input    string
		expected Level
	}{
		{"debug", LevelDebug},
		{"warn", LevelWarn},
		{"error", LevelError},
		{"info", LevelInfo},
		{"unknown", LevelInfo},
		{"", LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := parseLevel(tt.input)
			if result != tt.expected {
				t.Errorf("parseLevel(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNewHandler_JSON(t *testing.T) {
	var buf bytes.Buffer
	logger := newHandler(&buf, LevelInfo, "json")
	logger.Info("json test")

	output := buf.String()
	if !strings.Contains(output, `"msg"`) {
		t.Errorf("expected JSON handler output, got %q", output)
	}
}

func TestNewHandler_Text(t *testing.T) {
	var buf bytes.Buffer
	logger := newHandler(&buf, LevelInfo, "text")
	logger.Info("text test")

	output := buf.String()
	if !strings.Contains(output, "text test") {
		t.Errorf("expected text output containing message, got %q", output)
	}
}
