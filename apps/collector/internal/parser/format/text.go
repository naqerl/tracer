package format

import (
	"fmt"
	"regexp"

	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/libs/types"
)

type Text struct {
	source          string
	timestampRegex  *regexp.Regexp
	timestampFormat string
	levelRegex      *regexp.Regexp
	traceIDRegex    *regexp.Regexp
}

func NewText(cfg config.ResolvedApp) (*Text, error) {
	var tsRe, lvlRe, traceRe *regexp.Regexp
	var err error

	if cfg.TimestampRegex != "" {
		tsRe, err = regexp.Compile(cfg.TimestampRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid timestamp_regex: %w", err)
		}
	}
	if cfg.LevelRegex != "" {
		lvlRe, err = regexp.Compile(cfg.LevelRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid level_regex: %w", err)
		}
	}
	if cfg.TraceIDRegex != "" {
		traceRe, err = regexp.Compile(cfg.TraceIDRegex)
		if err != nil {
			return nil, fmt.Errorf("invalid trace_id_regex: %w", err)
		}
	}

	return &Text{
		source:          cfg.Source,
		timestampRegex:  tsRe,
		timestampFormat: cfg.TimestampFormat,
		levelRegex:      lvlRe,
		traceIDRegex:    traceRe,
	}, nil
}

func (p *Text) Parse(line string) (types.LogEntry, error) {
	entry := types.LogEntry{
		Source: p.source,
		Raw:    line,
		Extra:  make(map[string]string),
	}

	if p.timestampRegex == nil {
		return types.LogEntry{}, fmt.Errorf("timestamp_regex is required for text parser")
	}
	ts, ok := extractRegexValue(p.timestampRegex, line, "timestamp")
	if !ok {
		return types.LogEntry{}, fmt.Errorf("timestamp not found")
	}
	parsedTS, err := parseTimestampString(ts, p.timestampFormat)
	if err != nil {
		return types.LogEntry{}, fmt.Errorf("parse failed: %w", err)
	}
	entry.Timestamp = parsedTS

	if p.levelRegex != nil {
		if level, ok := extractRegexValue(p.levelRegex, line, "level"); ok {
			entry.Level = normalizeLevel(level)
		}
	}

	if p.traceIDRegex != nil {
		if traceID, ok := extractRegexValue(p.traceIDRegex, line, "trace_id"); ok {
			entry.TraceID = traceID
		}
	}

	entry.Message = line
	return entry, nil
}

func extractRegexValue(re *regexp.Regexp, line, groupName string) (string, bool) {
	match := re.FindStringSubmatch(line)
	if match == nil {
		return "", false
	}

	names := re.SubexpNames()
	for i, n := range names {
		if n == groupName && i < len(match) {
			return match[i], true
		}
	}

	if len(match) > 1 {
		return match[1], true
	}
	return "", false
}
