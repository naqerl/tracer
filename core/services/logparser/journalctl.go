package logparser

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	collectorconfig "github.com/ai-shift/tracer/config/collector"
)

// journalctlParser parses journalctl JSON output
// Use with: journalctl -u <service> -f -o json
//
// The parser extracts timestamp from __REALTIME_TIMESTAMP and level from PRIORITY.
// The MESSAGE field is stored as-is without additional parsing.
type journalctlParser struct {
	source       string
	unit         string
	levelField   string
	messageField string
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

func newJournalctlParser(cfg collectorconfig.App) (*journalctlParser, error) {
	p := &journalctlParser{
		source:       cfg.Path,
		unit:         cfg.Unit,
		levelField:   "PRIORITY", // Always from journalctl envelope
		messageField: "MESSAGE",  // Always from journalctl envelope
	}
	return p, nil
}

func (p *journalctlParser) Parse(line string) (LogEntry, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return LogEntry{}, fmt.Errorf("%w: %v", ErrParseFailed, err)
	}

	entry := LogEntry{
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

	// Extract and use raw MESSAGE
	if msg, ok := data[p.messageField]; ok {
		entry.Message = fmt.Sprintf("%v", msg)
		delete(data, p.messageField)
	} else {
		// If no message field found, use the entire raw log line
		entry.Message = line
	}

	entry.Level = journalLevel

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
