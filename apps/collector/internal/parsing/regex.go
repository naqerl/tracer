package logparser

import (
	"fmt"
	"regexp"
	"time"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
)

type regexParser struct {
	source          string
	pattern         *regexp.Regexp
	timestampField  string
	timestampFormat string
	levelField      string
	traceIDField    string
	messageField    string
}

var _ Parser = (*regexParser)(nil)

func newRegexParser(cfg collectorconfig.App) (*regexParser, error) {
	pattern, err := regexp.Compile(cfg.Pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPattern, err)
	}

	return &regexParser{
		source:          cfg.Path,
		pattern:         pattern,
		timestampField:  withDefault(cfg.TimestampField, "timestamp"),
		timestampFormat: cfg.TimestampFormat,
		levelField:      withDefault(cfg.LevelField, "level"),
		traceIDField:    withDefault(cfg.TraceIDField, "trace_id"),
		messageField:    withDefault(cfg.MessageField, "message"),
	}, nil
}

func (p *regexParser) Parse(line string) (LogEntry, error) {
	matches := p.pattern.FindStringSubmatch(line)
	if matches == nil {
		return LogEntry{}, fmt.Errorf("%w: no match", ErrParseFailed)
	}

	// Build map of named groups
	groups := make(map[string]string)
	for i, name := range p.pattern.SubexpNames() {
		if i > 0 && name != "" && i < len(matches) {
			groups[name] = matches[i]
		}
	}

	entry := LogEntry{
		Source: p.source,
		Raw:    line,
		Extra:  make(map[string]string),
	}

	// Extract known fields
	if ts, ok := groups[p.timestampField]; ok {
		entry.Timestamp = parseTimestampString(ts, p.timestampFormat)
		delete(groups, p.timestampField)
	} else {
		entry.Timestamp = time.Now()
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
		// If no message field found, use the entire raw log line
		entry.Message = line
	}

	// Store remaining named groups as extra context
	for k, v := range groups {
		entry.Extra[k] = v
	}

	return entry, nil
}
