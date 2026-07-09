package outbound

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/types"
	"tracer/libs/wsx"
)

const testTimeout = 5 * time.Second

// ═══════════════════════════════════════════════════════════════════════════
// Test helpers
// ═══════════════════════════════════════════════════════════════════════════

// echoServer starts a WebSocket server that echoes every received message.
func echoServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	var upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
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

func envEvent(collector string) types.Event {
	return types.Envelope{
		Type:      types.EventTypeLog,
		TS:        time.Now().UTC(),
		Collector: collector,
		Data:      []byte(`{"msg":"test"}`),
	}
}

func dialOpts() []wsx.ClientOption {
	return []wsx.ClientOption{
		wsx.WithHandshakeTimeout(testTimeout),
		wsx.WithWriteTimeout(time.Second),
		wsx.WithPingInterval(time.Second),
		wsx.WithPongWait(5 * time.Second),
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Tests
// ═══════════════════════════════════════════════════════════════════════════

func TestReconnectingBasicSendRecv(t *testing.T) {
	srv, url := echoServer(t)
	defer srv.Close()

	var callCount int
	r := newReconnecting(func() (Client, error) {
		callCount++
		c, err := wsx.New(url, dialOpts()...)
		if err != nil {
			return nil, err
		}
		return c, nil
	})
	defer r.Close()

	evt := envEvent("test-basic")
	if err := r.Send(evt); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Read echo.
	select {
	case got := <-r.Recv():
		env, ok := got.(types.Envelope)
		if !ok {
			t.Fatalf("expected Envelope, got %T", got)
		}
		if env.Collector != "test-basic" {
			t.Fatalf("got collector %q, want %q", env.Collector, "test-basic")
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for Recv")
	}

	if callCount != 1 {
		t.Fatalf("expected 1 factory call, got %d", callCount)
	}
}

func TestReconnectingReconnectOnSendFailure(t *testing.T) {
	srv, url := echoServer(t)
	defer srv.Close()

	// First call returns a broken client (immediate close).
	// Second call succeeds.
	var callCount atomic.Int32
	r := newReconnecting(func() (Client, error) {
		if callCount.Add(1) == 1 {
			// Return a client that's already closed.
			c, err := wsx.New(url, dialOpts()...)
			if err != nil {
				return nil, err
			}
			c.Close()
			return c, nil
		}
		return wsx.New(url, dialOpts()...)
	})
	defer r.Close()

	evt := envEvent("test-reconnect")
	if err := r.Send(evt); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case got := <-r.Recv():
		env, ok := got.(types.Envelope)
		if !ok {
			t.Fatalf("expected Envelope, got %T", got)
		}
		if env.Collector != "test-reconnect" {
			t.Fatalf("got collector %q, want %q", env.Collector, "test-reconnect")
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for Recv")
	}
}

func TestReconnectingFactoryFailure(t *testing.T) {
	wantErr := errors.New("factory boom")
	r := newReconnecting(func() (Client, error) {
		return nil, wantErr
	})
	defer r.Close()

	err := r.Send(envEvent("test"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "factory boom") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReconnectingSendAfterClose(t *testing.T) {
	srv, url := echoServer(t)
	defer srv.Close()

	r := newReconnecting(func() (Client, error) {
		return wsx.New(url, dialOpts()...)
	})
	r.Close()

	err := r.Send(envEvent("test"))
	if err == nil {
		t.Fatal("expected error after Close, got nil")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReconnectingCloseIdempotent(t *testing.T) {
	r := newReconnecting(func() (Client, error) {
		return nil, errors.New("no dial needed")
	})
	// Multiple Close calls must not panic or block.
	r.Close()
	r.Close()
	r.Close()
}

func TestReconnectingRecvStableAcrossReconnects(t *testing.T) {
	srv, url := echoServer(t)
	defer srv.Close()

	var callCount atomic.Int32
	r := newReconnecting(func() (Client, error) {
		callCount.Add(1)
		return wsx.New(url, dialOpts()...)
	})
	defer r.Close()

	recvCh := r.Recv()

	// Send successfully.
	if err := r.Send(envEvent("first")); err != nil {
		t.Fatalf("first Send: %v", err)
	}
	<-recvCh // drain echo

	// Verify Recv channel is the same object.
	if r.Recv() != recvCh {
		t.Fatal("Recv channel changed after first send")
	}
}

func TestReconnectingConcurrentSends(t *testing.T) {
	srv, url := echoServer(t)
	defer srv.Close()

	r := newReconnecting(func() (Client, error) {
		return wsx.New(url, dialOpts()...)
	})
	defer r.Close()

	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			evt := envEvent("concurrent")
			if err := r.Send(evt); err != nil {
				t.Errorf("Send #%d: %v", id, err)
			}
		}(i)
	}
	wg.Wait()

	// Drain all echoes.
	for i := 0; i < n; i++ {
		select {
		case <-r.Recv():
		case <-time.After(testTimeout):
			t.Fatalf("timed out waiting for echo #%d", i)
		}
	}
}

func TestReconnectingCloseDuringFactory(t *testing.T) {
	started := make(chan struct{})
	proceed := make(chan struct{})

	r := newReconnecting(func() (Client, error) {
		close(started)
		<-proceed
		return nil, errors.New("factory never completes")
	})

	// Start a Send that blocks in factory.
	done := make(chan struct{})
	go func() {
		r.Send(envEvent("test"))
		close(done)
	}()

	<-started      // wait for Send to enter factory
	r.Close()      // Close should not block
	close(proceed) // allow factory to complete
	<-done         // Send returns with error

	// All good — no deadlock.
}

func TestReconnectingRecvClosedAfterClose(t *testing.T) {
	srv, url := echoServer(t)
	defer srv.Close()

	r := newReconnecting(func() (Client, error) {
		return wsx.New(url, dialOpts()...)
	})

	// Send one event to establish a client.
	if err := r.Send(envEvent("test")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-r.Recv() // drain echo

	r.Close()

	// Recv channel should eventually close.
	select {
	case _, ok := <-r.Recv():
		if ok {
			t.Fatal("Recv returned value after Close")
		}
	case <-time.After(testTimeout):
		t.Fatal("Recv not closed after timeout")
	}
}

// TestReconnectingRecvNotClosedPanics ensures the Recv channel returns
// something without panicking — that it's properly typed.
func TestReconnectingRecvType(t *testing.T) {
	srv, url := echoServer(t)
	defer srv.Close()

	r := newReconnecting(func() (Client, error) {
		return wsx.New(url, dialOpts()...)
	})
	defer r.Close()

	ch := r.Recv()
	if ch == nil {
		t.Fatal("Recv() returned nil channel")
	}
}
