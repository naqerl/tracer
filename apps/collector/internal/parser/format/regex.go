package format

import (
	"fmt"
	"regexp"

	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/libs/types"
)

type Regex struct {
	source          string
	pattern         *regexp.Regexp
	timestampField  string
	timestampFormat string
	levelField      string
	traceIDField    string
	messageField    string
}

func NewRegex(cfg config.ResolvedApp) (*Regex, error) {
	pattern, err := regexp.Compile(cfg.Pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	return &Regex{
		source:          cfg.Source,
		pattern:         pattern,
		timestampField:  withDefault(cfg.TimestampField, "timestamp"),
		timestampFormat: cfg.TimestampFormat,
		levelField:      withDefault(cfg.LevelField, "level"),
		traceIDField:    withDefault(cfg.TraceIDField, "trace_id"),
		messageField:    withDefault(cfg.MessageField, "message"),
	}, nil
}

func (p *Regex) Parse(line string) (types.LogEntry, error) {
	matches := p.pattern.FindStringSubmatch(line)
	if matches == nil {
		return types.LogEntry{}, fmt.Errorf("no match")
	}

	groups := make(map[string]string)
	for i, name := range p.pattern.SubexpNames() {
		if i > 0 && name != "" && i < len(matches) {
			groups[name] = matches[i]
		}
	}

	entry := types.LogEntry{
		Source: p.source,
		Raw:    line,
		Extra:  make(map[string]string),
	}

	if ts, ok := groups[p.timestampField]; ok {
		parsedTS, err := parseTimestampString(ts, p.timestampFormat)
		if err != nil {
			return types.LogEntry{}, fmt.Errorf("parse failed: %w", err)
		}
		entry.Timestamp = parsedTS
		delete(groups, p.timestampField)
	} else {
		return types.LogEntry{}, fmt.Errorf("missing timestamp group %q", p.timestampField)
	}

	if level, ok := groups[p.levelField]; ok {
		entry.Level = normalizeLevel(level)
		delete(groups, p.levelField)
	}

	if traceID, ok := groups[p.traceIDField]; ok {
		entry.TraceID = traceID
		delete(groups, p.traceIDField)
	}

	if msg, ok := groups[p.messageField]; ok {
		entry.Message = msg
		delete(groups, p.messageField)
	} else {
		entry.Message = line
	}

	for k, v := range groups {
		entry.Extra[k] = v
	}

	return entry, nil
}
