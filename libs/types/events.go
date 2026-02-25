package types

import (
	"encoding/json"
	"fmt"
	"time"
)

type EventType string

const (
	EventTypeRegister EventType = "register"
	EventTypeLog      EventType = "log"
	EventTypeStats    EventType = "stats"
	EventTypeError    EventType = "error"
)

func (t EventType) Validate() error {
	switch t {
	case EventTypeRegister, EventTypeLog, EventTypeStats, EventTypeError:
		return nil
	}
	return fmt.Errorf("invalid event type: %q", t)
}

// Event is the base collector event interface.
type Event interface {
	EventType() EventType
	Timestamp() time.Time
}

// LogEvent represents a parsed log line.
type LogEvent struct {
	Time  time.Time `json:"timestamp"`
	App   string    `json:"app"`
	Entry LogEntry  `json:"entry"`
}

func (e LogEvent) EventType() EventType { return EventTypeLog }
func (e LogEvent) Timestamp() time.Time { return e.Time }

// StatsEvent represents system statistics snapshot.
// No App field - system-wide event.
type StatsEvent struct {
	Time time.Time `json:"timestamp"`
	Data Stats     `json:"stats"`
}

func (e StatsEvent) EventType() EventType { return EventTypeStats }
func (e StatsEvent) Timestamp() time.Time { return e.Time }

// Envelope is the wire format with collector context added by transport.
type Envelope struct {
	Type      EventType       `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Collector string          `json:"collector"`
	Data      json.RawMessage `json:"data"`
}

// RegisterMessage is the initial registration message.
type RegisterMessage struct {
	Type      EventType `json:"type"`
	Collector string    `json:"collector"`
	Apps      []string  `json:"apps"`
}

// ErrorEvent represents a source error for reporting to panel.
type ErrorEvent struct {
	Time    time.Time `json:"timestamp"`
	App     string    `json:"app"`
	ErrType string    `json:"error_type"` // e.g., "unhealthy", "restart_failed"
	Message string    `json:"message"`
	Source  string    `json:"source"`
}

func (e ErrorEvent) EventType() EventType { return EventTypeError }
func (e ErrorEvent) Timestamp() time.Time { return e.Time }
