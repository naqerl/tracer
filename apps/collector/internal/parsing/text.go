package logparser

import (
	"fmt"
	"regexp"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/libs/types"
)

type textParser struct {
	source          string
	timestampRegex  *regexp.Regexp
	timestampFormat string
	levelRegex      *regexp.Regexp
	traceIDRegex    *regexp.Regexp
}

var _ Parser = (*textParser)(nil)

func newTextParser(cfg collectorconfig.ResolvedApp) (*textParser, error) {
	var tsRe *regexp.Regexp
	var lvlRe *regexp.Regexp
	var traceRe *regexp.Regexp
	var err error

	if cfg.TimestampRegex != "" {
		tsRe, err = regexp.Compile(cfg.TimestampRegex)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid timestamp_regex: %v", ErrInvalidPattern, err)
		}
	}
	if cfg.LevelRegex != "" {
		lvlRe, err = regexp.Compile(cfg.LevelRegex)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid level_regex: %v", ErrInvalidPattern, err)
		}
	}
	if cfg.TraceIDRegex != "" {
		traceRe, err = regexp.Compile(cfg.TraceIDRegex)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid trace_id_regex: %v", ErrInvalidPattern, err)
		}
	}

	return &textParser{
		source:          cfg.Source,
		timestampRegex:  tsRe,
		timestampFormat: cfg.TimestampFormat,
		levelRegex:      lvlRe,
		traceIDRegex:    traceRe,
	}, nil
}

func (p *textParser) Parse(line string) (types.LogEntry, error) {
	entry := types.LogEntry{
		Source: p.source,
		Raw:    line,
		Extra:  make(map[string]string),
	}

	if p.timestampRegex == nil {
		return types.LogEntry{}, fmt.Errorf("%w: timestamp_regex is required for text parser", ErrParseFailed)
	}
	ts, ok := extractRegexValue(p.timestampRegex, line, "timestamp")
	if !ok {
		return types.LogEntry{}, fmt.Errorf("%w: timestamp not found", ErrParseFailed)
	}
	parsedTS, err := parseTimestampString(ts, p.timestampFormat)
	if err != nil {
		return types.LogEntry{}, fmt.Errorf("%w: %v", ErrParseFailed, err)
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

// extractRegexValue returns named group value if present, otherwise first capture group.
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
