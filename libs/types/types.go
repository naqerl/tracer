package types

import "time"

// Stats contains system statistics, shared between collector and panel via API
type Stats struct {
	CPUUsagePercent float64   `json:"cpu_usage_percent"`
	MemTotalMB      uint64    `json:"mem_total_mb"`
	MemUsedMB       uint64    `json:"mem_used_mb"`
	MemAvailableMB  uint64    `json:"mem_available_mb"`
	DiskTotalGB     uint64    `json:"disk_total_gb"`
	DiskUsedGB      uint64    `json:"disk_used_gb"`
	DiskAvailGB     uint64    `json:"disk_avail_gb"`
	Timestamp       time.Time `json:"timestamp"`
}

// LogEntry represents a parsed log line, shared between collector and panel via API
type LogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Level     string            `json:"level"`
	TraceID   string            `json:"trace_id"`
	Message   string            `json:"message"`
	Source    string            `json:"source"`
	Raw       string            `json:"raw"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// LogMessage is the message format from collectors to panel via WebSocket
type LogMessage struct {
	Type          string                 `json:"type"` // "register", "log", "startup", "stats"
	AppName       string                 `json:"app_name"`
	Apps          []string               `json:"apps,omitempty"`           // for registration
	CollectorName string                 `json:"collector_name,omitempty"` // for registration and stats
	Entry         LogEntry               `json:"entry,omitempty"`
	Stats         *Stats                 `json:"stats,omitempty"`
	Thresholds    *CollectorThresholds   `json:"thresholds,omitempty"` // for registration
}

// CollectorThresholds represents alert thresholds for a collector
type CollectorThresholds struct {
	CPU  float64 `json:"cpu"`
	Mem  float64 `json:"mem"`
	Disk float64 `json:"disk"`
}
