package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEnvelopeMarshalRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	orig := LogEvent{
		Time: now,
		App:  "nginx",
		Entry: LogEntry{
			Timestamp: now,
			Level:     LevelInfo,
			Message:   "request served",
		},
	}

	env := Envelope{
		Type:      EventTypeLog,
		Time:      now,
		Collector: "test-collector",
		Data:      orig,
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	var decoded Envelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}

	if decoded.Type != EventTypeLog {
		t.Fatalf("type: got %q, want %q", decoded.Type, EventTypeLog)
	}
	if decoded.Collector != "test-collector" {
		t.Fatalf("collector: got %q, want %q", decoded.Collector, "test-collector")
	}

	// Decode the data field (now any -> map[string]interface{})
	raw, err := json.Marshal(decoded.Data)
	if err != nil {
		t.Fatalf("marshal decoded data: %v", err)
	}
	var got LogEvent
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal decoded data: %v", err)
	}

	if got.App != "nginx" {
		t.Fatalf("app: got %q, want %q", got.App, "nginx")
	}
	if got.Entry.Message != "request served" {
		t.Fatalf("message: got %q, want %q", got.Entry.Message, "request served")
	}
}

func TestEnvelopeNilData(t *testing.T) {
	env := Envelope{
		Type:      EventTypeStats,
		Time:      time.Now().UTC(),
		Collector: "c1",
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Envelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Data != nil {
		t.Fatalf("expected nil Data, got %v", decoded.Data)
	}
}

func TestEnvelopeImplementsEvent(t *testing.T) {
	env := Envelope{Type: EventTypeHealth, Time: time.Now()}
	var _ Event = env
	_ = env.EventType()
	_ = env.Timestamp()
}
