package wsx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/types"
)

// testTimeout is a reasonable timeout for test operations.
const testTimeout = 5 * time.Second

// ── Helpers ────────────────────────────────────────────────────────────────────

// startTestServer starts a local WebSocket echo server for testing.
func startTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	var upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("test server upgrade error: %v", err)
			return
		}
		defer conn.Close()

		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			// Echo back
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}))

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	return srv, url
}

// startRegistrationServer starts a test server that expects a RegisterEvent
// on connect and echoes everything else.
func startRegistrationServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	var upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("test server upgrade error: %v", err)
			return
		}
		defer conn.Close()

		// Expect registration first
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg types.RegisterEvent
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Logf("test server: bad registration: %v", err)
			return
		}

		// Echo everything else
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}))

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	return srv, url
}

func connectOpts() []ClientOption {
	return []ClientOption{
		WithHandshakeTimeout(testTimeout),
		WithWriteTimeout(time.Second),
		WithPingInterval(10 * time.Second),
		WithPongWait(20 * time.Second),
	}
}

// waitForRecv attempts to read an event from client.Recv() within the timeout.
func waitForRecv(t *testing.T, client *Client) types.Event {
	t.Helper()
	select {
	case evt := <-client.Recv():
		return evt
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for event from Recv")
		return nil
	}
}

// ── Client: Send & Recv ────────────────────────────────────────────────────────

func TestClientSendRecv(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Close()

	client, err := New(url, connectOpts()...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	// Wait until Recv is ready by sending a message and reading its echo.
	payload, _ := json.Marshal(map[string]string{"msg": "hello"})
	env := types.Envelope{
		Type:      types.EventTypeLog,
		TS:        time.Now().UTC(),
		Collector: "test",
		Data:      payload,
	}

	if err := client.Send(env); err != nil {
		t.Fatalf("Send: %v", err)
	}

	evt := waitForRecv(t, client)
	got, ok := evt.(types.Envelope)
	if !ok {
		t.Fatalf("expected Envelope, got %T", evt)
	}
	if got.Type != types.EventTypeLog {
		t.Fatalf("got type %q, want %q", got.Type, types.EventTypeLog)
	}
	if got.Collector != "test" {
		t.Fatalf("got collector %q, want %q", got.Collector, "test")
	}
}

func TestClientSendRegisterRecv(t *testing.T) {
	srv, url := startRegistrationServer(t)
	defer srv.Close()

	client, err := New(url, connectOpts()...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	// Send registration first
	if err := client.Send(types.RegisterEvent{
		Type:      types.EventTypeRegister,
		Collector: "test-collector",
		Apps:      []string{"app1", "app2"},
	}); err != nil {
		t.Fatalf("Send registration: %v", err)
	}

	// Send an envelope and verify echo
	payload, _ := json.Marshal(map[string]int{"n": 42})
	env := types.Envelope{
		Type:      types.EventTypeStats,
		TS:        time.Now().UTC(),
		Collector: "test-collector",
		Data:      payload,
	}
	if err := client.Send(env); err != nil {
		t.Fatalf("Send: %v", err)
	}

	evt := waitForRecv(t, client)
	got, ok := evt.(types.Envelope)
	if !ok {
		t.Fatalf("expected Envelope, got %T", evt)
	}
	if got.Type != types.EventTypeStats {
		t.Fatalf("got type %q, want %q", got.Type, types.EventTypeStats)
	}
}

func TestClientConcurrentSends(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Close()

	client, err := New(url, connectOpts()...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			payload, _ := json.Marshal(map[string]int{"n": n})
			env := types.Envelope{
				Type:      types.EventTypeLog,
				TS:        time.Now().UTC(),
				Collector: "test",
				Data:      payload,
			}
			if err := client.Send(env); err != nil {
				t.Errorf("Send #%d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	// Read back all 10 messages (echos)
	for i := 0; i < 10; i++ {
		waitForRecv(t, client)
	}
}

// ── Client: Close behavior ─────────────────────────────────────────────────────

func TestClientCloseIdempotent(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Close()

	client, err := New(url, connectOpts()...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Close multiple times — must not panic.
	client.Close()
	client.Close()
}

func TestClientSendAfterClose(t *testing.T) {
	srv, url := startTestServer(t)
	defer srv.Close()

	client, err := New(url, connectOpts()...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	client.Close()

	// Send may succeed if the underlying connection was already closed
	// but shouldn't panic.
	_ = client.Send(types.Envelope{
		Type:      types.EventTypeLog,
		TS:        time.Now().UTC(),
		Collector: "test",
	})
}

// ── Upgrade (server mode) ──────────────────────────────────────────────────────

func TestServerModeSendRecv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, err := Upgrade(w, r,
			WithWriteTimeout(time.Second),
			WithPongWait(20*time.Second),
		)
		if err != nil {
			t.Logf("Upgrade error: %v", err)
			return
		}
		defer client.Close()

		// Echo back everything received
		for evt := range client.Recv() {
			_ = client.Send(evt)
		}
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	client, err := New(url, connectOpts()...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	payload, _ := json.Marshal(map[string]string{"hello": "world"})
	env := types.Envelope{
		Type:      types.EventTypeLog,
		TS:        time.Now().UTC(),
		Collector: "server-test",
		Data:      payload,
	}

	if err := client.Send(env); err != nil {
		t.Fatalf("Send: %v", err)
	}

	evt := waitForRecv(t, client)
	got, ok := evt.(types.Envelope)
	if !ok {
		t.Fatalf("expected Envelope, got %T", evt)
	}
	if got.Type != types.EventTypeLog {
		t.Fatalf("got type %q, want %q", got.Type, types.EventTypeLog)
	}
}
