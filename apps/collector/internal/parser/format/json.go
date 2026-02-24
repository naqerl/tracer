package format

import (
	"encoding/json"
	"fmt"

	"github.com/ai-shift/tracer/libs/types"
)

// JSON parses JSON formatted log lines.
type JSON struct {
	Schema Schema
}

func (p JSON) Parse(line string) (types.LogEntry, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return types.LogEntry{}, fmt.Errorf("parse failed: %w", err)
	}

	entry := types.LogEntry{
		Extra: make(map[string]string),
	}

	if ts, ok := data[p.Schema.TimestampField]; ok {
		parsedTS, err := parseTimestampValue(ts, p.Schema.TimestampFormat)
		if err != nil {
			return types.LogEntry{}, fmt.Errorf("parse failed: %w", err)
		}
		entry.Timestamp = parsedTS
		delete(data, p.Schema.TimestampField)
	} else {
		return types.LogEntry{}, fmt.Errorf("parse failed: missing timestamp field %q", p.Schema.TimestampField)
	}

	if level, ok := data[p.Schema.LevelField].(string); ok {
		entry.Level = normalizeLevel(level)
		delete(data, p.Schema.LevelField)
	}

	if traceID, ok := data[p.Schema.TraceIDField].(string); ok {
		entry.TraceID = traceID
		delete(data, p.Schema.TraceIDField)
	}

	if msg, ok := data[p.Schema.MessageField].(string); ok {
		entry.Message = msg
		delete(data, p.Schema.MessageField)
	} else {
		entry.Message = line
	}

	for k, v := range data {
		entry.Extra[k] = fmt.Sprintf("%v", v)
	}

	return entry, nil
}
