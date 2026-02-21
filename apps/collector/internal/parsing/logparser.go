package logparser

import (
	"strings"
	"time"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
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
func normalizeLevel(level string) string {
	normalized := strings.ToUpper(level)
	switch normalized {
	case "WARNING":
		return "WARN"
	case "CRITICAL":
		return "FATAL"
	default:
		return normalized
	}
}
