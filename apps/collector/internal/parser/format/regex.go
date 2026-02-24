package format

import (
	"fmt"
	"regexp"

	"github.com/ai-shift/tracer/libs/types"
)

type Regex struct {
	Pattern *regexp.Regexp
	Schema  Schema
}

func (p Regex) Parse(line string) (types.LogEntry, error) {
	matches := p.Pattern.FindStringSubmatch(line)
	if matches == nil {
		return types.LogEntry{}, fmt.Errorf("no match")
	}

	groups := make(map[string]string)
	for i, name := range p.Pattern.SubexpNames() {
		if i > 0 && name != "" && i < len(matches) {
			groups[name] = matches[i]
		}
	}

	entry := types.LogEntry{
		Extra: make(map[string]string),
	}

	if ts, ok := groups[p.Schema.TimestampField]; ok {
		parsedTS, err := parseTimestampString(ts, p.Schema.TimestampFormat)
		if err != nil {
			return types.LogEntry{}, fmt.Errorf("parse failed: %w", err)
		}
		entry.Timestamp = parsedTS
		delete(groups, p.Schema.TimestampField)
	} else {
		return types.LogEntry{}, fmt.Errorf("missing timestamp group %q", p.Schema.TimestampField)
	}

	if level, ok := groups[p.Schema.LevelField]; ok {
		entry.Level = normalizeLevel(level)
		delete(groups, p.Schema.LevelField)
	}

	if traceID, ok := groups[p.Schema.TraceIDField]; ok {
		entry.TraceID = traceID
		delete(groups, p.Schema.TraceIDField)
	}

	if msg, ok := groups[p.Schema.MessageField]; ok {
		entry.Message = msg
		delete(groups, p.Schema.MessageField)
	} else {
		entry.Message = line
	}

	for k, v := range groups {
		entry.Extra[k] = v
	}

	return entry, nil
}
