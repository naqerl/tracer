package wsx

import (
	"context"
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

func connectOpts(url string) []ClientOption {
	return []ClientOption{
		WithURL(url),
		WithHandshakeTimeout(testTimeout),
		WithWriteTimeout(time.Second),
		WithPingInterval(10 * time.Second),
		WithPongWait(20 * time.Second),
		WithReconnectDelay(time.Second),
		WithMaxReconnectWait(5 * time.Second),
	}
}

// ── Client: Send & Recv ────────────────────────────────────────────────────────

func TestClientSendRecv(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	connected := make(chan struct{})
	client := New(append(connectOpts(url),
		WithOnConnect(func(_ context.Context) error {
			close(connected)
			return nil
		}),
	)...)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	go func() {
		_ = client.Start(ctx)
	}()

	select {
	case <-connected:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for connect")
	}

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

	var client *Client

	connected := make(chan struct{})
	client = New(append(connectOpts(url),
		WithOnConnect(func(_ context.Context) error {
			close(connected)
			return client.Send(types.RegisterMessage{
				Collector: "test-collector",
				Apps:      []string{"app1", "app2"},
			})
		}),
	)...)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	go func() {
		_ = client.Start(ctx)
	}()

	select {
	case <-connected:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for connect")
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

	connected := make(chan struct{})
	client := New(append(connectOpts(url),
		WithOnConnect(func(_ context.Context) error {
			close(connected)
			return nil
		}),
	)...)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	go func() {
		_ = client.Start(ctx)
	}()

	select {
	case <-connected:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for connect")
	}

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

// ── Client: Reconnect ───────────────────────────────────────────────────────────

func TestClientReconnect(t *testing.T) {
	var mu sync.Mutex
	acceptCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		acceptCount++
		count := acceptCount
		mu.Unlock()

		var upgrader = websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		if count == 1 {
			conn.Close()
			return
		}
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	connected := make(chan struct{}, 2)
	client := New(
		WithURL(url),
		WithHandshakeTimeout(testTimeout),
		WithWriteTimeout(time.Second),
		WithPingInterval(10*time.Second),
		WithPongWait(20*time.Second),
		WithReconnectDelay(time.Second),
		WithMaxReconnectWait(2*time.Second),
		WithOnConnect(func(_ context.Context) error {
			connected <- struct{}{}
			return nil
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	go func() {
		_ = client.Start(ctx)
	}()

	select {
	case <-connected:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for first connect")
	}
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reconnect connect")
	}

	mu.Lock()
	if acceptCount < 2 {
		t.Fatalf("expected at least 2 connections, got %d", acceptCount)
	}
	mu.Unlock()
}

func TestClientContextCancel(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	client := New(connectOpts(url)...)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.Start(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestClientDisconnectedCallback(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	disconnected := make(chan struct{})
	connected := make(chan struct{})

	client := New(append(connectOpts(url),
		WithOnConnect(func(_ context.Context) error {
			close(connected)
			return nil
		}),
		WithOnDisconnect(func() {
			close(disconnected)
		}),
	)...)

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	go func() {
		_ = client.Start(ctx)
	}()

	select {
	case <-connected:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for connect")
	}

	cancel()

	select {
	case <-disconnected:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for OnDisconnect")
	}
}

// ── Server mode ────────────────────────────────────────────────────────────────

func TestServerModeSendRecv(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	dialer := websocket.Dialer{HandshakeTimeout: testTimeout}
	raw, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	client := New(WithConn(raw), WithWriteTimeout(time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	go func() {
		_ = client.Start(ctx)
	}()

	// Wait briefly for Start and any OnConnect
	time.Sleep(100 * time.Millisecond)

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

func TestSendBeforeStart(t *testing.T) {
	// Verify that Send returns an error when not connected
	client := New(WithURL("ws://127.0.0.1:1"))

	err := client.Send(types.RegisterMessage{Collector: "test"})
	if err == nil {
		t.Fatal("expected error when not connected")
	}
}
