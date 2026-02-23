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

	watchdog "github.com/ai-shift/tracer/apps/collector/internal/watch"
	"github.com/ai-shift/tracer/libs/types"
)

type receivedMessage struct {
	header string
	msg    types.LogMessage
}

func TestWSClientSendsHeaderRegistrationAndLog(t *testing.T) {
	t.Parallel()

	recvCh := make(chan receivedMessage, 4)
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
			var msg types.LogMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				errCh <- err
				return
			}
			recvCh <- receivedMessage{header: apiKey, msg: msg}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	logCh := make(chan watchdog.AppLogEntry, 2)
	client := New(wsURL, "secret-key", "collector-1", []string{"app-a"}, logCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Start(ctx)

	first := awaitWSMessage(t, recvCh, errCh, 4*time.Second)
	if first.header != "secret-key" {
		t.Fatalf("expected X-API-Key header, got %q", first.header)
	}
	if first.msg.Type != types.MessageTypeRegister {
		t.Fatalf("expected register message first, got %q", first.msg.Type)
	}
	if first.msg.CollectorName != "collector-1" {
		t.Fatalf("unexpected collector name: %q", first.msg.CollectorName)
	}
	if len(first.msg.Apps) != 1 || first.msg.Apps[0] != "app-a" {
		t.Fatalf("unexpected apps payload: %#v", first.msg.Apps)
	}

	logCh <- watchdog.AppLogEntry{
		AppName: "app-a",
		Entry: types.LogEntry{
			Timestamp: time.Now(),
			Level:     types.LevelInfo,
			Message:   "hello from test",
			Source:    "/tmp/test.log",
			Raw:       `{"message":"hello from test"}`,
		},
	}

	second := awaitWSMessage(t, recvCh, errCh, 4*time.Second)
	if second.msg.Type != types.MessageTypeLog {
		t.Fatalf("expected log message second, got %q", second.msg.Type)
	}
	if second.msg.AppName != "app-a" {
		t.Fatalf("unexpected app name: %q", second.msg.AppName)
	}
	if second.msg.Entry.Message != "hello from test" {
		t.Fatalf("unexpected log message payload: %q", second.msg.Entry.Message)
	}
}

func awaitWSMessage(t *testing.T, recv <-chan receivedMessage, errs <-chan error, timeout time.Duration) receivedMessage {
	t.Helper()
	select {
	case err := <-errs:
		t.Fatalf("websocket server error: %v", err)
	case got := <-recv:
		return got
	case <-time.After(timeout):
		t.Fatalf("timeout waiting for ws message")
	}
	return receivedMessage{}
}
