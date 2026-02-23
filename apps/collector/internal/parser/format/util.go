package format

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ai-shift/tracer/libs/types"
)

func normalizeLevel(level string) types.LogLevel {
	normalized := strings.ToUpper(level)
	switch normalized {
	case "":
		return types.LevelInfo
	case "EMERG", "ALERT", "CRIT", "CRITICAL", "FATAL", "PANIC":
		return types.LevelError
	case "ERR", "ERROR":
		return types.LevelError
	case "WARNING":
		return types.LevelWarn
	case "WARN":
		return types.LevelWarn
	case "NOTICE":
		return types.LevelInfo
	case "DEBUG":
		return types.LevelDebug
	case "INFO":
		return types.LevelInfo
	default:
		return types.LevelInfo
	}
}

func parseTimestampValue(v any, format string) (time.Time, error) {
	switch ts := v.(type) {
	case string:
		return parseTimestampString(ts, format)
	case float64:
		return parseTimestampNumber(ts)
	case int64:
		return parseTimestampNumber(float64(ts))
	case int:
		return parseTimestampNumber(float64(ts))
	default:
		return time.Time{}, fmt.Errorf("unsupported timestamp type: %T", v)
	}
}

func parseTimestampString(ts string, format string) (time.Time, error) {
	if format == "" {
		return time.Time{}, fmt.Errorf("timestamp_format is required for string timestamps")
	}
	t, err := time.Parse(format, ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q with format %q: %w", ts, format, err)
	}
	return t, nil
}

func parseTimestampNumber(ts float64) (time.Time, error) {
	abs := math.Abs(ts)
	switch {
	case abs >= 1e18:
		return time.Unix(0, int64(ts)), nil
	case abs >= 1e15:
		return time.UnixMicro(int64(ts)), nil
	case abs >= 1e12:
		return time.UnixMilli(int64(ts)), nil
	default:
		sec := int64(ts)
		nsec := int64((ts - float64(sec)) * 1e9)
		return time.Unix(sec, nsec), nil
	}
}
