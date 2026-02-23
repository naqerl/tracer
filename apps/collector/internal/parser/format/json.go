package format

import (
	"encoding/json"
	"fmt"

	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/libs/types"
)

// JSON parses JSON formatted log lines.
type JSON struct {
	source          string
	timestampField  string
	timestampFormat string
	levelField      string
	traceIDField    string
	messageField    string
}

// NewJSON creates a new JSON parser.
func NewJSON(cfg config.ResolvedApp) (*JSON, error) {
	return &JSON{
		source:          cfg.Source,
		timestampField:  cfg.TimestampField,
		timestampFormat: cfg.TimestampFormat,
		levelField:      cfg.LevelField,
		traceIDField:    cfg.TraceIDField,
		messageField:    cfg.MessageField,
	}, nil
}

func (p *JSON) Parse(line string) (types.LogEntry, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return types.LogEntry{}, fmt.Errorf("parse failed: %w", err)
	}

	entry := types.LogEntry{
		Source: p.source,
		Raw:    line,
		Extra:  make(map[string]string),
	}

	if ts, ok := data[p.timestampField]; ok {
		parsedTS, err := parseTimestampValue(ts, p.timestampFormat)
		if err != nil {
			return types.LogEntry{}, fmt.Errorf("parse failed: %w", err)
		}
		entry.Timestamp = parsedTS
		delete(data, p.timestampField)
	} else {
		return types.LogEntry{}, fmt.Errorf("parse failed: missing timestamp field %q", p.timestampField)
	}

	if level, ok := data[p.levelField].(string); ok {
		entry.Level = normalizeLevel(level)
		delete(data, p.levelField)
	}

	if traceID, ok := data[p.traceIDField].(string); ok {
		entry.TraceID = traceID
		delete(data, p.traceIDField)
	}

	if msg, ok := data[p.messageField].(string); ok {
		entry.Message = msg
		delete(data, p.messageField)
	} else {
		entry.Message = line
	}

	for k, v := range data {
		entry.Extra[k] = fmt.Sprintf("%v", v)
	}

	return entry, nil
}
