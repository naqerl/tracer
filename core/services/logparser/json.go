package logparser

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ai-shift/tracer/config"
)

type jsonParser struct {
	source         string
	timestampField string
	levelField     string
	traceIDField   string
	messageField   string
}

var _ Parser = (*jsonParser)(nil)

func newJSONParser(cfg config.LogFile) (*jsonParser, error) {
	return &jsonParser{
		source:         cfg.Path,
		timestampField: withDefault(cfg.TimestampField, "timestamp"),
		levelField:     withDefault(cfg.LevelField, "level"),
		traceIDField:   withDefault(cfg.TraceIDField, "trace_id"),
		messageField:   withDefault(cfg.MessageField, "message"),
	}, nil
}

func (p *jsonParser) Parse(line string) (*LogEntry, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	entry := &LogEntry{
		Source: p.source,
		Raw:    line,
		Extra:  make(map[string]string),
	}

	// Extract known fields
	if ts, ok := data[p.timestampField]; ok {
		entry.Timestamp = parseTimestamp(ts)
		delete(data, p.timestampField)
	} else {
		entry.Timestamp = time.Now()
	}

	if level, ok := data[p.levelField].(string); ok {
		entry.Level = level
		delete(data, p.levelField)
	}

	if traceID, ok := data[p.traceIDField].(string); ok {
		entry.TraceID = traceID
		delete(data, p.traceIDField)
	}

	if msg, ok := data[p.messageField].(string); ok {
		entry.Message = msg
		delete(data, p.messageField)
	}

	// Store remaining fields as extra context
	for k, v := range data {
		entry.Extra[k] = fmt.Sprintf("%v", v)
	}

	return entry, nil
}

func parseTimestamp(v any) time.Time {
	switch ts := v.(type) {
	case string:
		// Try common formats
		formats := []string{
			time.RFC3339,
			time.RFC3339Nano,
			"2006-01-02T15:04:05",
			"2006-01-02 15:04:05",
		}
		for _, f := range formats {
			if t, err := time.Parse(f, ts); err == nil {
				return t
			}
		}
	case float64:
		// Unix timestamp (seconds or milliseconds)
		if ts > 1e12 {
			return time.UnixMilli(int64(ts))
		}
		return time.Unix(int64(ts), 0)
	}
	return time.Now()
}

func withDefault(val, def string) string {
	if val == "" {
		return def
	}
	return val
}
