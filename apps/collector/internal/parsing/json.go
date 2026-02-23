package logparser

import (
	"encoding/json"
	"fmt"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/libs/types"
)

type jsonParser struct {
	source          string
	timestampField  string
	timestampFormat string
	levelField      string
	traceIDField    string
	messageField    string
}

var _ Parser = (*jsonParser)(nil)

func newJSONParser(cfg collectorconfig.ResolvedApp) (*jsonParser, error) {
	return &jsonParser{
		source:          cfg.Source,
		timestampField:  withDefault(cfg.TimestampField, "timestamp"),
		timestampFormat: cfg.TimestampFormat,
		levelField:      withDefault(cfg.LevelField, "level"),
		traceIDField:    withDefault(cfg.TraceIDField, "trace_id"),
		messageField:    withDefault(cfg.MessageField, "message"),
	}, nil
}

func (p *jsonParser) Parse(line string) (types.LogEntry, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return types.LogEntry{}, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	entry := types.LogEntry{
		Source: p.source,
		Raw:    line,
		Extra:  make(map[string]string),
	}

	// Extract known fields
	if ts, ok := data[p.timestampField]; ok {
		parsedTS, err := parseTimestampValue(ts, p.timestampFormat)
		if err != nil {
			return types.LogEntry{}, fmt.Errorf("%w: %v", ErrParseFailed, err)
		}
		entry.Timestamp = parsedTS
		delete(data, p.timestampField)
	} else {
		return types.LogEntry{}, fmt.Errorf("%w: missing timestamp field %q", ErrParseFailed, p.timestampField)
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
		// If no message field found, use the entire raw log line
		entry.Message = line
	}

	// Store remaining fields as extra context
	for k, v := range data {
		entry.Extra[k] = fmt.Sprintf("%v", v)
	}

	return entry, nil
}

func withDefault(val, def string) string {
	if val == "" {
		return def
	}
	return val
}
