package logparser

import "time"

var defaultTimestampFormats = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006/01/02 15:04:05",
	"2006-01-02 15:04:05,000",
	"02/Jan/2006:15:04:05 -0700",
	"2006/01/02 15:04:05",
	"Jan 02 15:04:05",
	"Jan  2 15:04:05",
	"2006-01-02 15:04:05.000 MST",
}

func parseTimestampValue(v any, format string) time.Time {
	switch ts := v.(type) {
	case string:
		return parseTimestampString(ts, format)
	case float64:
		if ts > 1e12 {
			return time.UnixMilli(int64(ts))
		}
		sec := int64(ts)
		nsec := int64((ts - float64(sec)) * 1e9)
		return time.Unix(sec, nsec)
	case int64:
		if ts > 1e12 {
			return time.UnixMilli(ts)
		}
		return time.Unix(ts, 0)
	case int:
		if ts > 1e12 {
			return time.UnixMilli(int64(ts))
		}
		return time.Unix(int64(ts), 0)
	default:
		return time.Now()
	}
}

func parseTimestampString(ts string, format string) time.Time {
	if format != "" {
		if t, err := time.Parse(format, ts); err == nil {
			return t
		}
		return time.Now()
	}

	for _, f := range defaultTimestampFormats {
		if t, err := time.Parse(f, ts); err == nil {
			return t
		}
	}
	return time.Now()
}
