package logparser

import (
	"fmt"
	"math"
	"time"
)

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

// parseTimestampNumber parses epoch timestamps by magnitude:
// seconds (<1e12), milliseconds (>=1e12), microseconds (>=1e15), nanoseconds (>=1e18).
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
