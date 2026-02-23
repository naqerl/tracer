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
func New(cfg collectorconfig.ResolvedApp) (Parser, error) {
	switch cfg.ParserKind {
	case "json":
		return newJSONParser(cfg)
	case "regex":
		return newRegexParser(cfg)
	case "text":
		return newTextParser(cfg)
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
	case "":
		return types.LevelInfo
	case "EMERG", "ALERT", "CRIT", "CRITICAL", "FATAL", "PANIC":
		return types.LevelError
	case "ERR", "ERROR":
		return types.LevelError
	case "WARNING":
		return types.LevelWarn
	case "WARN":
		return types.LevelWarn
	case "NOTICE":
		return types.LevelInfo
	case "DEBUG":
		return types.LevelDebug
	case "INFO":
		return types.LevelInfo
	default:
		// Keep collector/panel contract strict to known level enum values.
		return types.LevelInfo
	}
}
