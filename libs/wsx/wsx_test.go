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
// It returns the server, its WebSocket URL, and a channel that receives
// every message the server receives (as raw bytes).
func startTestServer(t *testing.T) (*httptest.Server, string, chan []byte) {
	t.Helper()

	msgCh := make(chan []byte, 100)
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
			msgCh <- data
			// Echo back
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}))

	// Derive ws:// URL from http:// URL
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	return srv, url, msgCh
}

// startRegisterServer starts a test server that expects a registration message
// on connect and echoes everything else.
func startRegisterServer(t *testing.T) (*httptest.Server, string) {
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
		var msg types.RegisterMessage
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

// ── Client: Send & Recv ────────────────────────────────────────────────────────

func TestClientSendRecv(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	client, err := New(url, connectOpts()...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	// Give the read/ping loops a moment to start
	time.Sleep(50 * time.Millisecond)

	// Send an Envelope via the Event interface
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

	// Read echo via Recv
	select {
	case evt := <-client.Recv():
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
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for echo")
	}
}

func TestClientSendRegisterRecv(t *testing.T) {
	srv, url := startRegisterServer(t)
	defer srv.Close()

	client, err := New(url, connectOpts()...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	time.Sleep(50 * time.Millisecond)

	// Send registration first
	if err := client.Send(types.RegisterMessage{
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

	select {
	case evt := <-client.Recv():
		got, ok := evt.(types.Envelope)
		if !ok {
			t.Fatalf("expected Envelope, got %T", evt)
		}
		if got.Type != types.EventTypeStats {
			t.Fatalf("got type %q, want %q", got.Type, types.EventTypeStats)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for echo")
	}
}

func TestClientConcurrentSends(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	client, err := New(url, connectOpts()...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	time.Sleep(50 * time.Millisecond)

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
		select {
		case <-client.Recv():
		case <-time.After(testTimeout):
			t.Fatal("timed out waiting for echo")
		}
	}
}

// ── Upgrade (server mode) ──────────────────────────────────────────────────────

func TestUpgradeSendRecv(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	// Dial raw connection, then use Upgrade to wrap it
	dialer := websocket.Dialer{HandshakeTimeout: testTimeout}
	raw, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Simulate the panel side: use Upgrade from an HTTP request by
	// constructing one. We skip the HTTP roundtrip here since we already
	// have a raw connection — we test Upgrade path separately below.
	//
	// Instead, test via direct dial + raw wrap (see TestServerModeSendRecv).

	_ = raw.Close()

	// For now, test New (which uses dial) — Upgrade testing would
	// require an httptest.Server with the upgrade handler.
}

// TestServerModeSendRecv tests the upgrade path by manually dialing and
// wrapping with a http.Handler that calls wsx.Upgrade.
func TestServerModeSendRecv(t *testing.T) {
	msgCh := make(chan []byte, 100)
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
			payload, _ := json.Marshal(evt)
			msgCh <- payload
			_ = client.Send(evt)
		}
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Dial as a regular WebSocket client
	client, err := New(url, connectOpts()...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer client.Close()

	time.Sleep(50 * time.Millisecond)

	// Send an event
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

	// Read echo
	select {
	case evt := <-client.Recv():
		got, ok := evt.(types.Envelope)
		if !ok {
			t.Fatalf("expected Envelope, got %T", evt)
		}
		if got.Type != types.EventTypeLog {
			t.Fatalf("got type %q, want %q", got.Type, types.EventTypeLog)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for echo")
	}
}
