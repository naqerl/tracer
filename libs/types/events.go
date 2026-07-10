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
)

func (t EventType) Validate() error {
	switch t {
	case EventTypeRegister, EventTypeLog, EventTypeLifecycle, EventTypeStats, EventTypeError, EventTypeHealth:
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
	Type      EventType `json:"type"`
	Time      time.Time `json:"timestamp"`
	Collector string    `json:"collector"`
	Data      any       `json:"data"`
}

func (e Envelope) EventType() EventType { return e.Type }
func (e Envelope) Timestamp() time.Time { return e.Time }

// DecodeData converts the envelope's Data field (any-typed JSON-decoded value)
// into the typed target. It is a marshal-unmarshal roundtrip that handles both
// the case where Data is already a typed struct and where it is a map from
// json.Unmarshal.
func (e Envelope) DecodeData(target any) error {
	raw, err := json.Marshal(e.Data)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

// RegisterEvent is the initial registration event sent on connect.
type RegisterEvent struct {
	Type      EventType `json:"type"`
	Collector string    `json:"collector"`
	Apps      []string  `json:"apps"`
}

func (r RegisterEvent) EventType() EventType { return EventTypeRegister }
func (r RegisterEvent) Timestamp() time.Time { return time.Time{} }

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
