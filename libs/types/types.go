package types

import (
	"fmt"
	"time"
)

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
	Level     LogLevel          `json:"level"`
	TraceID   string            `json:"trace_id"`
	Message   string            `json:"message"`
	Extra     map[string]string `json:"extra,omitempty"`
}

// LogPayload is the internal collector representation of a log entry.
// It carries enrichment metadata (AppName, IsStartup) alongside the parsed entry.
type LogPayload struct {
	AppName   string   `json:"app_name"`
	Entry     LogEntry `json:"entry"`
	IsStartup bool     `json:"is_startup"`
}

// StatsPayload is the message format for stats from collectors to panel via WebSocket
type StatsPayload struct {
	Type          string `json:"type"` // "stats"
	AppName       string `json:"app_name"`
	CollectorName string `json:"collector_name"`
	Stats         *Stats `json:"stats"`
}

// MessageType represents the type of message sent from collector to panel
type MessageType string

const (
	MessageTypeRegister MessageType = "register"
	MessageTypeLog      MessageType = "log"
	MessageTypeStartup  MessageType = "startup"
	MessageTypeStats    MessageType = "stats"
)

// Validate checks if the message type is valid
func (mt MessageType) Validate() error {
	switch mt {
	case MessageTypeRegister, MessageTypeLog, MessageTypeStartup, MessageTypeStats:
		return nil
	}
	return fmt.Errorf("invalid message type: %s", mt)
}

// LogMessage is the message format from collectors to panel via WebSocket
type LogMessage struct {
	Type          MessageType `json:"type"` // "register", "log", "startup", "stats"
	AppName       string      `json:"app_name"`
	Apps          []string    `json:"apps,omitempty"`           // for registration
	CollectorName string      `json:"collector_name,omitempty"` // for registration and stats
	Entry         LogEntry    `json:"entry,omitempty"`
	Stats         *Stats      `json:"stats,omitempty"`
}
