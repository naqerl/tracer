package types

import "time"

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
