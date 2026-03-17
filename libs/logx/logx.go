package logx

import (
	"log/slog"
	"os"
	"strings"
)

// parseLogLevel parses a log level string (case-insensitive) into slog.Level.
// Defaults to Info if the level is unrecognized.
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// DefaultLogger creates a production-ready Text logger writing to stdout.
// The log level is determined by the LOG_LEVEL environment variable (case-insensitive).
// Supported levels: debug, info, warn/warning, error. Defaults to info.
func DefaultLogger() *slog.Logger {
	level := parseLogLevel(os.Getenv("LOG_LEVEL"))
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
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
