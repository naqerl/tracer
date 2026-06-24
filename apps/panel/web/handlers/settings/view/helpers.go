package view

import "time"

// formatTimestamp formats a Unix timestamp as a readable string.
func formatTimestamp(ts int64) string {
	return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
}

// tern provides a ternary operator for use in templates.
func tern(condition bool, a, b string) string {
	if condition {
		return a
	}
	return b
}
