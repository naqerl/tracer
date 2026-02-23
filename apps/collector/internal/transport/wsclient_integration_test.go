package wsclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ai-shift/tracer/apps/collector/internal/events"
	"github.com/ai-shift/tracer/libs/types"
)

type receivedEnvelope struct {
	header string
	env    types.Envelope
}

func TestWSClientSendsHeaderRegistrationAndLog(t *testing.T) {
	t.Parallel()

	recvCh := make(chan receivedEnvelope, 4)
	errCh := make(chan error, 1)

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("X-API-Key")
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()

		for i := 0; i < 2; i++ {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			var env types.Envelope
			if err := json.Unmarshal(payload, &env); err != nil {
				// Try register message format
				var reg types.RegisterMessage
				if err2 := json.Unmarshal(payload, &reg); err2 == nil {
					recvCh <- receivedEnvelope{header: apiKey, env: types.Envelope{Type: reg.Type}}
					continue
				}
				errCh <- err
				return
			}
			recvCh <- receivedEnvelope{header: apiKey, env: env}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	bus := events.NewBus(10)
	client := New(wsURL, "secret-key", "collector-1", []string{"app-a"}, bus)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Start(ctx)

	first := awaitWSMessage(t, recvCh, errCh, 4*time.Second)
	if first.header != "secret-key" {
		t.Fatalf("expected X-API-Key header, got %q", first.header)
	}
	if first.env.Type != "register" {
		t.Fatalf("expected register message first, got %q", first.env.Type)
	}

	// Publish a log event
	bus.Publish(types.LogEvent{
		Time: time.Now(),
		App:  "app-a",
		Entry: types.LogEntry{
			Timestamp: time.Now(),
			Level:     types.LevelInfo,
			Message:   "hello from test",
			Source:    "/tmp/test.log",
			Raw:       `{"message":"hello from test"}`,
		},
	})

	second := awaitWSMessage(t, recvCh, errCh, 4*time.Second)
	if second.env.Type != "log" {
		t.Fatalf("expected log message second, got %q", second.env.Type)
	}
	if second.env.App != "app-a" {
		t.Fatalf("unexpected app name: %q", second.env.App)
	}
}

func awaitWSMessage(t *testing.T, recv <-chan receivedEnvelope, errs <-chan error, timeout time.Duration) receivedEnvelope {
	t.Helper()
	select {
	case err := <-errs:
		t.Fatalf("websocket server error: %v", err)
	case got := <-recv:
		return got
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for ws message")
	}
	return receivedEnvelope{}
}
