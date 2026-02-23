package parser

import (
	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/parser/format"
	"github.com/ai-shift/tracer/libs/types"
)

// Parser parses log lines into structured LogEntry
type Parser interface {
	Parse(line string) (types.LogEntry, error)
}

// New creates a parser based on the app configuration
func New(cfg config.ResolvedApp) (Parser, error) {
	switch cfg.ParserKind {
	case "json":
		return format.NewJSON(cfg)
	case "regex":
		return format.NewRegex(cfg)
	case "text":
		return format.NewText(cfg)
	default:
		return nil, ErrUnknownFormat
	}
}
