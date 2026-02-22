package logparser

import (
	"strings"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/libs/types"
)

// Parser parses log lines into structured LogEntry
type Parser interface {
	Parse(line string) (types.LogEntry, error)
}

// New creates a parser based on the app configuration
func New(cfg collectorconfig.App) (Parser, error) {
	switch cfg.Format {
	case "json":
		return newJSONParser(cfg)
	case "regex":
		return newRegexParser(cfg)
	case "journalctl":
		return newJournalctlParser(cfg)
	default:
		return nil, ErrUnknownFormat
	}
}

// normalizeLevel converts various level formats to uppercase standard
func normalizeLevel(level string) types.LogLevel {
	normalized := strings.ToUpper(level)
	switch normalized {
	case "WARNING":
		return types.LevelWarn
	case "CRITICAL":
		return types.LevelError
	case "DEBUG":
		return types.LevelDebug
	case "INFO":
		return types.LevelInfo
	case "ERROR":
		return types.LevelError
	case "WARN":
		return types.LevelWarn
	default:
		return types.LogLevel(normalized)
	}
}
