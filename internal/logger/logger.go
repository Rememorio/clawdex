// Package logger provides a centralized logging facility using log/slog.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
)

// Level represents log severity.
type Level = slog.Level

// Log levels.
const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

var (
	defaultLogger *slog.Logger
	defaultLevel  = LevelInfo
	defaultFormat = "text"
)

func init() {
	defaultLogger = newHandler(os.Stderr, defaultLevel, defaultFormat)
}

func newHandler(w io.Writer, level Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

// Config holds logger configuration.
type Config struct {
	Level  string // debug, info, warn, error
	Format string // text, json
}

// Configure sets up the global logger from config.
func Configure(cfg Config) {
	level := parseLevel(cfg.Level)
	format := cfg.Format
	if format == "" {
		format = "text"
	}
	defaultLevel = level
	defaultFormat = format
	defaultLogger = newHandler(os.Stderr, level, format)
}

func parseLevel(s string) Level {
	switch s {
	case "debug":
		return LevelDebug
	case "warn":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// SetLevel configures the global log level.
func SetLevel(level Level) {
	defaultLevel = level
	defaultLogger = newHandler(os.Stderr, level, defaultFormat)
}

// SetOutput configures the output writer for the default logger.
func SetOutput(w io.Writer) {
	defaultLogger = newHandler(w, defaultLevel, defaultFormat)
}

// Debug logs a message at debug level.
func Debug(msg string, args ...any) {
	defaultLogger.Debug(msg, args...)
}

// DebugContext logs a message at debug level with context.
func DebugContext(ctx context.Context, msg string, args ...any) {
	defaultLogger.DebugContext(ctx, msg, args...)
}

// Info logs a message at info level.
func Info(msg string, args ...any) {
	defaultLogger.Info(msg, args...)
}

// InfoContext logs a message at info level with context.
func InfoContext(ctx context.Context, msg string, args ...any) {
	defaultLogger.InfoContext(ctx, msg, args...)
}

// Warn logs a message at warn level.
func Warn(msg string, args ...any) {
	defaultLogger.Warn(msg, args...)
}

// WarnContext logs a message at warn level with context.
func WarnContext(ctx context.Context, msg string, args ...any) {
	defaultLogger.WarnContext(ctx, msg, args...)
}

// Error logs a message at error level.
func Error(msg string, args ...any) {
	defaultLogger.Error(msg, args...)
}

// ErrorContext logs a message at error level with context.
func ErrorContext(ctx context.Context, msg string, args ...any) {
	defaultLogger.ErrorContext(ctx, msg, args...)
}

// With returns a logger with additional key-value pairs.
func With(args ...any) *slog.Logger {
	return defaultLogger.With(args...)
}

// L returns the default logger.
func L() *slog.Logger {
	return defaultLogger
}
