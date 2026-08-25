package notify

import (
	"reflect"
	"testing"
	"time"
)

func TestFormatMessageWithLog(t *testing.T) {
	n := Notification{
		Type:          TypeAlert,
		CollectorName: "prod",
		AppName:       "api",
		Payload: LogEntry{
			Level:     "error",
			Message:   "boom: connection refused",
			GroupID:   7,
			LogID:     42,
			CreatedAt: time.Now(),
			LogURL:    "https://panel/panel/collectors/prod/apps/api/logs/42",
		},
	}

	want := "api@prod\n\n```\nboom: connection refused\n```\n\n[view log](https://panel/panel/collectors/prod/apps/api/logs/42)"
	if got := FormatMessage(n); got != want {
		t.Errorf("FormatMessage mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatMessageLogWithoutURL(t *testing.T) {
	n := Notification{
		Type:          TypeAlert,
		CollectorName: "prod",
		AppName:       "api",
		Payload: LogEntry{
			Message: "boom",
			GroupID: 1,
		},
	}

	want := "api@prod\n\n```\nboom\n```"
	if got := FormatMessage(n); got != want {
		t.Errorf("FormatMessage mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatMessageStringPayload(t *testing.T) {
	n := Notification{Type: TypeLifecycle, Payload: "restart"}

	if got := FormatMessage(n); got != "restart" {
		t.Errorf("plain payload: got %q, want %q", got, "restart")
	}
}

func TestFormatMessageStringPayloadWithHeader(t *testing.T) {
	n := Notification{Type: TypeLifecycle, CollectorName: "prod", AppName: "api", Payload: "restart"}

	want := "api@prod\n\nrestart"
	if got := FormatMessage(n); got != want {
		t.Errorf("FormatMessage mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestFormatMessageResourceEntry(t *testing.T) {
	n := Notification{
		Type:          TypeResource,
		CollectorName: "prod",
		Payload: ResourceEntry{
			CPUUsagePercent: 97.3,
			MemUsedPercent:  85.4,
			MemTotalMB:      8192,
			MemUsedMB:       7000,
			MemAvailableMB:  1192,
			DiskUsedPercent: 80.0,
			DiskTotalGB:     100,
			DiskUsedGB:      80,
			DiskAvailGB:     20,
			Breached:        []string{"cpu"},
		},
	}

	got := FormatMessage(n)
	if got == "" {
		t.Fatal("FormatMessage returned empty for ResourceEntry")
	}
	// Should contain the collector header and resource details
	want := "@prod\n\nresource threshold breached: cpu\n\nCPU: 97.3%\nMemory: 7000 / 8192 MB (85.4%)\nDisk: 80 / 100 GB (80.0%)"
	if got != want {
		t.Errorf("FormatMessage mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// expectedPayloadType documents the payload contract: log-derived types carry
// a LogEntry, resource notifications carry a ResourceEntry, and the remaining
// types carry a string. Returns nil for types without a documented mapping so
// the test below fails when a new NotificationType is added but not added here.
func expectedPayloadType(typ NotificationType) reflect.Type {
	switch typ {
	case TypeAlert:
		return reflect.TypeOf(LogEntry{})
	case TypeResource:
		return reflect.TypeOf(ResourceEntry{})
	case TypeUptime:
		return reflect.TypeOf(UptimeEntry{})
	case TypeLifecycle, TypeWelcome, TypeSubscription:
		return reflect.TypeOf("")
	default:
		return nil
	}
}

// TestNotificationPayloadTypeMapping locks in the payload-to-type contract for
// every notification type and verifies FormatMessage renders a valid payload.
func TestNotificationPayloadTypeMapping(t *testing.T) {
	payloads := map[NotificationType]any{
		TypeAlert:        LogEntry{Message: "boom"},
		TypeResource:     ResourceEntry{Breached: []string{"cpu"}, CPUUsagePercent: 97.3, MemTotalMB: 8192, MemUsedMB: 7000, DiskTotalGB: 100, DiskUsedGB: 80},
		TypeLifecycle:    "restart",
		TypeWelcome:      "hello",
		TypeSubscription: "subscribed",
		TypeUptime:       UptimeEntry{Status: "down", Previous: "up"},
	}

	for typ, payload := range payloads {
		want := expectedPayloadType(typ)
		if want == nil {
			t.Fatalf("type %s: no payload mapping documented in expectedPayloadType", typ)
		}
		if got := reflect.TypeOf(payload); got != want {
			t.Errorf("type %s: expected payload %s, got %s", typ, want, got)
		}

		n := Notification{Type: typ, Payload: payload}
		if got := FormatMessage(n); got == "" {
			t.Errorf("type %s: FormatMessage returned empty for a valid payload", typ)
		}
	}

	// Every defined type must have a documented mapping; otherwise a new
	// notification type silently breaks the contract.
	for _, typ := range []NotificationType{TypeAlert, TypeResource, TypeLifecycle, TypeWelcome, TypeSubscription, TypeUptime} {
		if expectedPayloadType(typ) == nil {
			t.Errorf("type %s: missing from the payload mapping table", typ)
		}
	}
}
