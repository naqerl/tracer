package logparser

import (
	"time"

	"github.com/ai-shift/tracer/config"
)

// LogEntry represents a parsed log line
type LogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Level     string            `json:"level"`
	TraceID   string            `json:"trace_id"`
	Message   string            `json:"message"`
	Source    string            `json:"source"`
	Raw       string            `json:"raw"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// Parser parses log lines into structured LogEntry
type Parser interface {
	Parse(line string) (LogEntry, error)
}

// New creates a parser based on the app configuration
func New(cfg config.App) (Parser, error) {
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

// TODO: Simplify (at least case lol)
// normalizeLevel converts various level formats to uppercase standard
func normalizeLevel(level string) string {
	switch level {
	case "INFO", "info":
		return "INFO"
	case "WARN", "warn", "WARNING", "warning":
		return "WARN"
	case "ERROR", "error":
		return "ERROR"
	case "DEBUG", "debug":
		return "DEBUG"
	case "TRACE", "trace":
		return "TRACE"
	case "FATAL", "fatal", "CRITICAL", "critical":
		return "FATAL"
	default:
		return level
	}
}
