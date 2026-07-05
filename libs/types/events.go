package types

import (
	"encoding/json"
	"fmt"
	"time"
)

type EventType string

const (
	EventTypeRegister  EventType = "register"
	EventTypeLog       EventType = "log"
	EventTypeLifecycle EventType = "lifecycle"
	EventTypeStats     EventType = "stats"
	EventTypeError     EventType = "error"
	EventTypeHealth    EventType = "health"
	EventTypeUpdate    EventType = "update"
)

func (t EventType) Validate() error {
	switch t {
	case EventTypeRegister, EventTypeLog, EventTypeLifecycle, EventTypeStats, EventTypeError, EventTypeHealth, EventTypeUpdate:
		return nil
	}
	return fmt.Errorf("invalid event type: %q", t)
}

// MessageHeader is the JSON envelope used for incoming WebSocket messages
// to identify the event type. It is shared between collector and panel.
type MessageHeader struct {
	Type EventType `json:"type"`
}

func (m MessageHeader) EventType() EventType { return m.Type }
func (m MessageHeader) Timestamp() time.Time { return time.Time{} }

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

type LifecycleKind string

const (
	LifecycleStart   LifecycleKind = "start"
	LifecycleRestart LifecycleKind = "restart"
	LifecycleExit    LifecycleKind = "exit"
)

func (k LifecycleKind) Validate() error {
	switch k {
	case LifecycleStart, LifecycleRestart, LifecycleExit:
		return nil
	}
	return fmt.Errorf("invalid lifecycle kind: %q", k)
}

type LifecycleEvent struct {
	Time    time.Time     `json:"timestamp"`
	App     string        `json:"app"`
	Kind    LifecycleKind `json:"kind"`
	RawLine string        `json:"raw_line"`
}

func (e LifecycleEvent) EventType() EventType { return EventTypeLifecycle }
func (e LifecycleEvent) Timestamp() time.Time { return e.Time }

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
	Time      time.Time       `json:"timestamp"`
	Collector string          `json:"collector"`
	Data      json.RawMessage `json:"data"`
}

func (e Envelope) EventType() EventType { return e.Type }
func (e Envelope) Timestamp() time.Time { return e.Time }

// RegisterMessage is the initial registration message.
type RegisterMessage struct {
	Type      EventType `json:"type"`
	Collector string    `json:"collector"`
	Apps      []string  `json:"apps"`
}

func (m RegisterMessage) EventType() EventType { return m.Type }
func (m RegisterMessage) Timestamp() time.Time { return time.Time{} }

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

type HealthEvent struct {
	Time   time.Time    `json:"timestamp"`
	App    string       `json:"app"`
	Status HealthStatus `json:"status"`
}

func (e HealthEvent) EventType() EventType { return EventTypeHealth }
func (e HealthEvent) Timestamp() time.Time { return e.Time }

// UpdateRequestedEvent is sent from panel to collectors to trigger a self-update.
type UpdateRequestedEvent struct {
	Type    EventType `json:"type"`
	Version string    `json:"version,omitempty"`
}

func (m UpdateRequestedEvent) EventType() EventType { return m.Type }
func (m UpdateRequestedEvent) Timestamp() time.Time { return time.Time{} }

// FromJSON parses JSON bytes into the appropriate Event concrete type
// based on the "type" field.
func FromJSON(data []byte) (Event, error) {
	var hdr MessageHeader
	if err := json.Unmarshal(data, &hdr); err != nil {
		return nil, err
	}
	switch hdr.Type {
	case EventTypeRegister:
		var msg RegisterMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return msg, nil
	case EventTypeUpdate:
		var evt UpdateRequestedEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			return nil, err
		}
		return evt, nil
	default:
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, err
		}
		return env, nil
	}
}
