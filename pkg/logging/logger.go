// Package logging provides structured logging for Sentinel.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
)

// Level represents log levels.
type Level = slog.Level

const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// Logger wraps slog.Logger with convenience methods.
type Logger struct {
	*slog.Logger
}

// Config configures the logger.
type Config struct {
	Level  Level
	Format string // "json" or "text"
	Output io.Writer
}

// DefaultConfig returns default logger configuration.
func DefaultConfig() *Config {
	return &Config{
		Level:  LevelInfo,
		Format: "text",
		Output: os.Stdout,
	}
}

// New creates a new structured logger.
func New(cfg *Config) *Logger {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if cfg.Output == nil {
		cfg.Output = os.Stdout
	}

	opts := &slog.HandlerOptions{
		Level: cfg.Level,
	}

	var handler slog.Handler
	if cfg.Format == "json" {
		handler = slog.NewJSONHandler(cfg.Output, opts)
	} else {
		handler = slog.NewTextHandler(cfg.Output, opts)
	}

	return &Logger{slog.New(handler)}
}

// WithComponent returns a logger with a component field.
func (l *Logger) WithComponent(name string) *Logger {
	return &Logger{l.With("component", name)}
}

// WithNode returns a logger with a node field.
func (l *Logger) WithNode(name string) *Logger {
	return &Logger{l.With("node", name)}
}

// WithError returns a logger with an error field.
func (l *Logger) WithError(err error) *Logger {
	return &Logger{l.With("error", err.Error())}
}

// WithFields returns a logger with additional fields.
func (l *Logger) WithFields(fields map[string]any) *Logger {
	args := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		args = append(args, k, v)
	}
	return &Logger{l.With(args...)}
}

// Global logger instance
var defaultLogger = New(nil)

// SetDefault sets the default logger.
func SetDefault(l *Logger) {
	defaultLogger = l
	slog.SetDefault(l.Logger)
}

// Default returns the default logger.
func Default() *Logger {
	return defaultLogger
}

// Context key for logger
type ctxKey struct{}

// FromContext retrieves the logger from context.
func FromContext(ctx context.Context) *Logger {
	if l, ok := ctx.Value(ctxKey{}).(*Logger); ok {
		return l
	}
	return defaultLogger
}

// WithContext adds the logger to context.
func WithContext(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// Convenience functions using default logger

func Debug(msg string, args ...any) {
	defaultLogger.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	defaultLogger.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	defaultLogger.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	defaultLogger.Error(msg, args...)
}
