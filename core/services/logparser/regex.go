package logparser

import (
	"fmt"
	"regexp"
	"time"

	"github.com/ai-shift/tracer/config"
)

type regexParser struct {
	source         string
	pattern        *regexp.Regexp
	timestampField string
	levelField     string
	traceIDField   string
	messageField   string
}

var _ Parser = (*regexParser)(nil)

func newRegexParser(cfg config.App) (*regexParser, error) {
	pattern, err := regexp.Compile(cfg.Pattern)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPattern, err)
	}

	return &regexParser{
		source:         cfg.Path,
		pattern:        pattern,
		timestampField: withDefault(cfg.TimestampField, "timestamp"),
		levelField:     withDefault(cfg.LevelField, "level"),
		traceIDField:   withDefault(cfg.TraceIDField, "trace_id"),
		messageField:   withDefault(cfg.MessageField, "message"),
	}, nil
}

func (p *regexParser) Parse(line string) (*LogEntry, error) {
	matches := p.pattern.FindStringSubmatch(line)
	if matches == nil {
		return nil, fmt.Errorf("%w: no match", ErrParseFailed)
	}

	// Build map of named groups
	groups := make(map[string]string)
	for i, name := range p.pattern.SubexpNames() {
		if i > 0 && name != "" && i < len(matches) {
			groups[name] = matches[i]
		}
	}

	entry := &LogEntry{
		Source: p.source,
		Raw:    line,
		Extra:  make(map[string]string),
	}

	// Extract known fields
	if ts, ok := groups[p.timestampField]; ok {
		entry.Timestamp = parseTimestampString(ts)
		delete(groups, p.timestampField)
	} else {
		entry.Timestamp = time.Now()
	}

	if level, ok := groups[p.levelField]; ok {
		entry.Level = level
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

func parseTimestampString(ts string) time.Time {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006/01/02 15:04:05",         // Go stdlib log
		"2006-01-02 15:04:05,000",     // Python logging
		"02/Jan/2006:15:04:05 -0700",  // nginx access
		"2006/01/02 15:04:05",         // nginx error
		"Jan 02 15:04:05",             // syslog
		"Jan  2 15:04:05",             // syslog (single digit day)
		"2006-01-02 15:04:05.000 MST", // PostgreSQL
	}

	// Handle Python logging format with comma separator (e.g., "2026-01-06 09:50:49,370")
	// by replacing comma with dot for parsing
	normalizedTs := ts
	if len(ts) >= 23 && ts[19] == ',' {
		normalizedTs = ts[:19] + "." + ts[20:]
	}

	for _, f := range formats {
		if t, err := time.Parse(f, normalizedTs); err == nil {
			return t
		}
		// Also try original timestamp
		if normalizedTs != ts {
			if t, err := time.Parse(f, ts); err == nil {
				return t
			}
		}
	}
	return time.Now()
}
