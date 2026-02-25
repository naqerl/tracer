package parser

import (
	"github.com/ai-shift/tracer/libs/types"
)

// Parser parses log lines into structured LogEntry
type Parser interface {
	Parse(line string) (types.LogEntry, error)
}

type Spec struct {
	ID     string            `toml:"id"`
	Type   string            `toml:"type"`
	Params map[string]string `toml:"params"`
}

func New(spec Spec) Parser {
	panic("not implemented")
}
