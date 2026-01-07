package logparser

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/ai-shift/tracer/config"
)

// journalctlParser parses journalctl JSON output
// Use with: journalctl -u <service> -f -o json
//
// The parser extracts timestamp from __REALTIME_TIMESTAMP and level from PRIORITY.
// The MESSAGE field can be further parsed using an optional regex pattern with named groups.
//
// Example pattern for uvicorn logs:
//
//	`(?P<level>\w+):\s+[\d.:]+\s+-\s+(?P<message>.+)`
//
// This extracts level and message from: "INFO:     127.0.0.1:8000 - "GET /health HTTP/1.1" 200"
type journalctlParser struct {
	source         string
	unit           string
	levelField     string
	messageField   string
	pattern        *regexp.Regexp
	patternNames   []string
	timestampField string
	traceIDField   string
}

var _ Parser = (*journalctlParser)(nil)

// journalctl priority levels (syslog priority)
var journalctlPriorityToLevel = map[string]string{
	"0": "EMERG",
	"1": "ALERT",
	"2": "CRIT",
	"3": "ERROR",
	"4": "WARN",
	"5": "NOTICE",
	"6": "INFO",
	"7": "DEBUG",
}

func newJournalctlParser(cfg config.App) (*journalctlParser, error) {
	p := &journalctlParser{
		source:         cfg.Path,
		unit:           cfg.Unit,
		levelField:     withDefault(cfg.LevelField, "PRIORITY"),
		messageField:   withDefault(cfg.MessageField, "MESSAGE"),
		timestampField: cfg.TimestampField,
		traceIDField:   cfg.TraceIDField,
	}

	// Compile optional pattern for parsing MESSAGE content
	if cfg.Pattern != "" {
		re, err := regexp.Compile(cfg.Pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern: %w", err)
		}
		p.pattern = re
		p.patternNames = re.SubexpNames()
	}

	return p, nil
}

func (p *journalctlParser) Parse(line string) (*LogEntry, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	entry := &LogEntry{
		Source: p.source,
		Raw:    line,
		Extra:  make(map[string]string),
	}

	// Extract timestamp from __REALTIME_TIMESTAMP (microseconds since epoch)
	if ts, ok := data["__REALTIME_TIMESTAMP"]; ok {
		entry.Timestamp = parseJournalctlTimestamp(ts)
		delete(data, "__REALTIME_TIMESTAMP")
	} else {
		entry.Timestamp = time.Now()
	}

	// Extract priority/level from journalctl PRIORITY field
	var journalLevel string
	if priority, ok := data[p.levelField]; ok {
		priorityStr := fmt.Sprintf("%v", priority)
		if level, exists := journalctlPriorityToLevel[priorityStr]; exists {
			journalLevel = level
		} else {
			journalLevel = priorityStr
		}
		delete(data, p.levelField)
	}

	// Extract and parse MESSAGE
	if msg, ok := data[p.messageField]; ok {
		msgStr := fmt.Sprintf("%v", msg)
		delete(data, p.messageField)

		// If pattern is configured, parse the message content
		if p.pattern != nil {
			if parsed := p.parseMessage(msgStr); parsed != nil {
				// Extract level from pattern (overrides journalctl PRIORITY)
				if level, ok := parsed["level"]; ok && level != "" {
					entry.Level = normalizeLevel(level)
					delete(parsed, "level")
				}
				// Extract message from pattern
				if message, ok := parsed["message"]; ok {
					entry.Message = message
					delete(parsed, "message")
				}
				// Extract timestamp from pattern (overrides journalctl timestamp)
				if p.timestampField != "" {
					if ts, ok := parsed[p.timestampField]; ok {
						// Try to parse timestamp
						delete(parsed, p.timestampField)
						_ = ts // TODO: parse if needed
					}
				}
				// Extract trace_id from pattern
				if p.traceIDField != "" {
					if traceID, ok := parsed[p.traceIDField]; ok {
						entry.TraceID = traceID
						delete(parsed, p.traceIDField)
					}
				}
				// Store remaining parsed fields as extra
				for k, v := range parsed {
					if v != "" {
						entry.Extra[k] = v
					}
				}
			} else {
				// Pattern didn't match, use raw message
				entry.Message = msgStr
			}
		} else {
			// No pattern, use raw message
			entry.Message = msgStr
		}
	} else {
		// If no message field found, use the entire raw log line
		entry.Message = line
	}

	// Use journalctl level if not set from pattern
	if entry.Level == "" {
		entry.Level = journalLevel
	}

	// Extract useful metadata from journalctl
	usefulFields := []string{
		"SYSLOG_IDENTIFIER",
		"_SYSTEMD_UNIT",
		"_PID",
		"_HOSTNAME",
		"_COMM",
	}

	for _, field := range usefulFields {
		if v, ok := data[field]; ok {
			entry.Extra[field] = fmt.Sprintf("%v", v)
			delete(data, field)
		}
	}

	// Clean up internal journalctl fields we don't need
	internalFields := []string{
		"__CURSOR",
		"__MONOTONIC_TIMESTAMP",
		"__SEQNUM",
		"__SEQNUM_ID",
		"_BOOT_ID",
		"_CAP_EFFECTIVE",
		"_CMDLINE",
		"_EXE",
		"_GID",
		"_MACHINE_ID",
		"_RUNTIME_SCOPE",
		"_SELINUX_CONTEXT",
		"_STREAM_ID",
		"_SYSTEMD_CGROUP",
		"_SYSTEMD_INVOCATION_ID",
		"_SYSTEMD_SLICE",
		"_TRANSPORT",
		"_UID",
		"SYSLOG_FACILITY",
	}
	for _, field := range internalFields {
		delete(data, field)
	}

	// Store remaining fields as extra context
	for k, v := range data {
		entry.Extra[k] = fmt.Sprintf("%v", v)
	}

	return entry, nil
}

// parseMessage applies the regex pattern to extract named groups from message
func (p *journalctlParser) parseMessage(msg string) map[string]string {
	match := p.pattern.FindStringSubmatch(msg)
	if match == nil {
		return nil
	}

	result := make(map[string]string)
	for i, name := range p.patternNames {
		if name != "" && i < len(match) {
			result[name] = match[i]
		}
	}
	return result
}

func parseJournalctlTimestamp(v any) time.Time {
	var microseconds int64

	switch ts := v.(type) {
	case string:
		var err error
		microseconds, err = strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return time.Now()
		}
	case float64:
		microseconds = int64(ts)
	case int64:
		microseconds = ts
	case int:
		microseconds = int64(ts)
	default:
		return time.Now()
	}

	// __REALTIME_TIMESTAMP is in microseconds since epoch
	return time.UnixMicro(microseconds)
}
