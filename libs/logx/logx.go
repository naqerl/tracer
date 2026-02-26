package logx

import (
	"log/slog"
	"os"
)

// DefaultLogger creates a production-ready Text logger writing to stdout.
func DefaultLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// SetDefault sets the global default logger to a Text logger.
func SetDefault() {
	slog.SetDefault(DefaultLogger())
}

// DefaultLoggerWithLevel creates a Text logger with a custom level.
func DefaultLoggerWithLevel(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
}

// SetDefaultWithLevel sets the global default logger with a custom level.
func SetDefaultWithLevel(level slog.Level) {
	slog.SetDefault(DefaultLoggerWithLevel(level))
}
