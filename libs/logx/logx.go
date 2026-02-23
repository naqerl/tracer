package logx

import (
	"log/slog"
	"os"
)

// DefaultLogger creates a production-ready JSON logger writing to stdout.
func DefaultLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// SetDefault sets the global default logger to a JSON logger.
func SetDefault() {
	slog.SetDefault(DefaultLogger())
}

// DefaultLoggerWithLevel creates a JSON logger with a custom level.
func DefaultLoggerWithLevel(level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
}

// SetDefaultWithLevel sets the global default logger with a custom level.
func SetDefaultWithLevel(level slog.Level) {
	slog.SetDefault(DefaultLoggerWithLevel(level))
}
