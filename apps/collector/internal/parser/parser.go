package parser

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ai-shift/tracer/apps/collector/internal/parser/format"
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
	t := strings.ToLower(strings.TrimSpace(spec.Type))

	schema := format.Schema{
		LevelField:      requiredParam(spec, "level_field"),
		TraceIDField:    requiredParam(spec, "trace_id_field"),
		MessageField:    requiredParam(spec, "message_field"),
		TimestampField:  requiredParam(spec, "timestamp_field"),
		TimestampFormat: requiredParam(spec, "timestamp_format"),
	}

	switch t {
	case "json":
		return format.JSON{Schema: schema}
	case "regex":
		pattern := requiredParam(spec, "pattern")
		re, err := regexp.Compile(pattern)
		if err != nil {
			panic(fmt.Sprintf("parser %q: %v: %v", spec.ID, ErrInvalidPattern, err))
		}
		return format.Regex{Pattern: re, Schema: schema}
	default:
		panic(fmt.Sprintf("parser %q: %v: %s", spec.ID, ErrUnknownFormat, spec.Type))
	}
}

func requiredParam(spec Spec, key string) string {
	if spec.Params == nil {
		panic(fmt.Sprintf("parser %q: missing params.%s", spec.ID, key))
	}
	if v := strings.TrimSpace(spec.Params[key]); v != "" {
		return v
	}
	panic(fmt.Sprintf("parser %q: missing params.%s", spec.ID, key))
}
