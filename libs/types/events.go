package types

import (
	"encoding/json"
	"time"
)

// Event is the interface for all collector events.
// Events are created by sources/parsers with App context,
// then wrapped by transport with Collector context.
type Event interface {
	EventType() string
	AppName() string
	Timestamp() time.Time
}

// BaseEvent contains common fields for all events.
// Not used directly - embedded in concrete types.
type BaseEvent struct {
	EvtType string    `json:"-"`
	EvtApp  string    `json:"-"`
	EvtTime time.Time `json:"-"`
}

func (b BaseEvent) EventType() string    { return b.EvtType }
func (b BaseEvent) AppName() string      { return b.EvtApp }
func (b BaseEvent) Timestamp() time.Time { return b.EvtTime }

// LogEvent represents a parsed log line.
type LogEvent struct {
	Time  time.Time `json:"timestamp"`
	App   string    `json:"app"`
	Entry LogEntry  `json:"entry"`
}

func (e LogEvent) EventType() string    { return "log" }
func (e LogEvent) AppName() string      { return e.App }
func (e LogEvent) Timestamp() time.Time { return e.Time }

// StartupEvent represents application startup detection.
type StartupEvent struct {
	Time    time.Time `json:"timestamp"`
	App     string    `json:"app"`
	Message string    `json:"message"`
}

func (e StartupEvent) EventType() string    { return "startup" }
func (e StartupEvent) AppName() string      { return e.App }
func (e StartupEvent) Timestamp() time.Time { return e.Time }

// ShutdownEvent represents application shutdown detection.
type ShutdownEvent struct {
	Time   time.Time `json:"timestamp"`
	App    string    `json:"app"`
	Reason string    `json:"reason"`
}

func (e ShutdownEvent) EventType() string    { return "shutdown" }
func (e ShutdownEvent) AppName() string      { return e.App }
func (e ShutdownEvent) Timestamp() time.Time { return e.Time }

// StatsEvent represents system statistics snapshot.
// No App field - system-wide event.
type StatsEvent struct {
	Time time.Time `json:"timestamp"`
	Data Stats     `json:"stats"`
}

func (e StatsEvent) EventType() string    { return "stats" }
func (e StatsEvent) AppName() string      { return "" } // System-wide
func (e StatsEvent) Timestamp() time.Time { return e.Time }

// Envelope is the wire format with collector context added by transport.
type Envelope struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Collector string          `json:"collector"`
	App       string          `json:"app"`
	Data      json.RawMessage `json:"data"`
}

// RegisterMessage is the initial registration message.
type RegisterMessage struct {
	Type      string   `json:"type"`
	Collector string   `json:"collector"`
	Apps      []string `json:"apps"`
}

// ErrorEvent represents a source error for reporting to panel.
type ErrorEvent struct {
	Time    time.Time `json:"timestamp"`
	App     string    `json:"app"`
	ErrType string    `json:"error_type"` // e.g., "unhealthy", "restart_failed"
	Message string    `json:"message"`
	Source  string    `json:"source"`
}

func (e ErrorEvent) EventType() string    { return "error" }
func (e ErrorEvent) AppName() string      { return e.App }
func (e ErrorEvent) Timestamp() time.Time { return e.Time }

// RawLine is the input from source listeners (before parsing).
type RawLine struct {
	App  string
	Line string
}
