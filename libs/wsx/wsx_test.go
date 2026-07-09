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

// ── Conn Tests ──────────────────────────────────────────────────────────────────

func TestConnWriteJSON(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	dialer := websocket.Dialer{HandshakeTimeout: testTimeout}
	raw, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer raw.Close()

	conn := NewConn(raw, time.Second)

	payload := map[string]string{"hello": "world"}
	if err := conn.WriteJSON(payload); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	// Read echo
	_, data, err := raw.ReadMessage()
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}

	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["hello"] != "world" {
		t.Fatalf("got %v, want hello=world", got)
	}
}

func TestConnSendJSON(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	dialer := websocket.Dialer{HandshakeTimeout: testTimeout}
	raw, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer raw.Close()

	conn := NewConn(raw, time.Second)
	if err := conn.SendJSON(map[string]int{"n": 42}); err != nil {
		t.Fatalf("SendJSON: %v", err)
	}

	_, data, err := raw.ReadMessage()
	if err != nil {
		t.Fatalf("read echo: %v", err)
	}
	var got map[string]int
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["n"] != 42 {
		t.Fatalf("got n=%d, want 42", got["n"])
	}
}

func TestConnConcurrentWrites(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	dialer := websocket.Dialer{HandshakeTimeout: testTimeout}
	raw, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer raw.Close()

	conn := NewConn(raw, time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			payload := map[string]int{"n": n}
			if err := conn.WriteJSON(payload); err != nil {
				t.Errorf("WriteJSON #%d: %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	// Read back all 10 messages (echos)
	for i := 0; i < 10; i++ {
		_, data, err := raw.ReadMessage()
		if err != nil {
			t.Fatalf("read echo #%d: %v", i, err)
		}
		var got map[string]int
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
}

func TestConnClose(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	dialer := websocket.Dialer{HandshakeTimeout: testTimeout}
	raw, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	conn := NewConn(raw, time.Second)
	if err := conn.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Subsequent writes should fail
	if err := conn.WriteJSON(map[string]string{"x": "y"}); err == nil {
		t.Fatal("expected write after close to fail")
	}
}

// ── Client Tests ────────────────────────────────────────────────────────────────

func TestClientConnectAndSend(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	received := make(chan []byte, 10)
	handler := func(msgType int, data []byte) {
		received <- data
	}

	client := NewClient(Config{
		URL:              url,
		HandshakeTimeout: testTimeout,
		WriteTimeout:     time.Second,
		PingInterval:     10 * time.Second,
		PongWait:         20 * time.Second,
		ReconnectDelay:   time.Second,
		MaxReconnectWait: 5 * time.Second,
		ReadHandler:      handler,
	})

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	connected := make(chan struct{})
	client.config.OnConnect = func(_ context.Context, conn *Conn) error {
		close(connected)
		return nil
	}

	go func() {
		_ = client.Start(ctx)
	}()

	select {
	case <-connected:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for connect")
	}

	// Send a message
	if err := client.SendJSON(map[string]string{"msg": "hello"}); err != nil {
		t.Fatalf("SendJSON: %v", err)
	}

	// Read echo via handler
	select {
	case msg := <-received:
		var got map[string]string
		if err := json.Unmarshal(msg, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["msg"] != "hello" {
			t.Fatalf("got %v, want msg=hello", got)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for echo")
	}
}

func TestClientReconnect(t *testing.T) {
	// Create a server that accepts one connection then dies.
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

		// First connection: die immediately
		if count == 1 {
			conn.Close()
			return
		}
		// Subsequent connections: stay alive
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
	client := NewClient(Config{
		URL:              url,
		HandshakeTimeout: testTimeout,
		WriteTimeout:     time.Second,
		PingInterval:     10 * time.Second,
		PongWait:         20 * time.Second,
		ReconnectDelay:   time.Second,
		MaxReconnectWait: 2 * time.Second,
		OnConnect: func(_ context.Context, conn *Conn) error {
			connected <- struct{}{}
			return nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	go func() {
		_ = client.Start(ctx)
	}()

	// Expect at least two connects: first fails, reconnect succeeds
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

	client := NewClient(Config{
		URL:              url,
		HandshakeTimeout: testTimeout,
		WriteTimeout:     time.Second,
		PingInterval:     10 * time.Second,
		PongWait:         20 * time.Second,
		ReconnectDelay:   time.Second,
		MaxReconnectWait: 5 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Immediate cancellation

	err := client.Start(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestClientDisconnectedCallback(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	disconnected := make(chan struct{})

	client := NewClient(Config{
		URL:              url,
		HandshakeTimeout: testTimeout,
		WriteTimeout:     time.Second,
		PingInterval:     10 * time.Second,
		PongWait:         20 * time.Second,
		ReconnectDelay:   time.Second,
		MaxReconnectWait: 5 * time.Second,
		OnDisconnect: func() {
			close(disconnected)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	connected := make(chan struct{})
	client.config.OnConnect = func(_ context.Context, conn *Conn) error {
		close(connected)
		return nil
	}

	go func() {
		_ = client.Start(ctx)
	}()

	select {
	case <-connected:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for connect")
	}

	// Cancel context to trigger disconnect
	cancel()

	select {
	case <-disconnected:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for OnDisconnect")
	}
}

func TestClientReadHandler(t *testing.T) {
	srv, url, _ := startTestServer(t)
	defer srv.Close()

	received := make(chan []byte, 10)
	client := NewClient(Config{
		URL:              url,
		HandshakeTimeout: testTimeout,
		WriteTimeout:     time.Second,
		PingInterval:     10 * time.Second,
		PongWait:         20 * time.Second,
		ReconnectDelay:   time.Second,
		MaxReconnectWait: 5 * time.Second,
		ReadHandler: func(msgType int, data []byte) {
			received <- data
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	connected := make(chan struct{})
	client.config.OnConnect = func(_ context.Context, conn *Conn) error {
		close(connected)
		return nil
	}

	go func() {
		_ = client.Start(ctx)
	}()

	select {
	case <-connected:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for connect")
	}

	// Send message via the underlying connection (simulate server push)
	conn := client.Conn()
	if conn == nil {
		t.Fatal("conn is nil after connect")
	}
	if err := conn.SendJSON(map[string]string{"push": "data"}); err != nil {
		t.Fatalf("SendJSON: %v", err)
	}

	select {
	case msg := <-received:
		var got map[string]string
		if err := json.Unmarshal(msg, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["push"] != "data" {
			t.Fatalf("got %v, want push=data", got)
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for read handler")
	}
}
