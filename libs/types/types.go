package types

import "time"

// LogLevel represents the severity level of a log entry
type LogLevel string

const (
	LevelDebug LogLevel = "debug"
	LevelInfo  LogLevel = "info"
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
)

// User is the authenticated user principal shared across layers.
// It intentionally excludes persistence-only fields like password hashes.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"is_admin"`
}

// Stats contains system statistics, shared between collector and panel via API
type Stats struct {
	CPUUsagePercent float64   `json:"cpu_usage_percent"`
	MemTotalMB      int64     `json:"mem_total_mb"`
	MemUsedMB       int64     `json:"mem_used_mb"`
	MemAvailableMB  int64     `json:"mem_available_mb"`
	DiskTotalGB     int64     `json:"disk_total_gb"`
	DiskUsedGB      int64     `json:"disk_used_gb"`
	DiskAvailGB     int64     `json:"disk_avail_gb"`
	Timestamp       time.Time `json:"timestamp"`
}

// LogEntry represents a parsed log line, shared between collector and panel via API
type LogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Level     LogLevel          `json:"level"`
	TraceID   string            `json:"trace_id"`
	Message   string            `json:"message"`
	Extra     map[string]string `json:"extra,omitempty"`
}
