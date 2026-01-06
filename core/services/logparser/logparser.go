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
	Parse(line string) (*LogEntry, error)
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
