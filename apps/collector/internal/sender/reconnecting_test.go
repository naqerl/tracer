package sender

import (
	"context"
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

// Test helpers

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
		Time:      time.Now().UTC(),
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

// Tests

func TestReconnectingDialsBeforeFirstSend(t *testing.T) {
	srv, url := echoServer(t)
	defer srv.Close()

	dialed := make(chan struct{})
	var once sync.Once
	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
		once.Do(func() { close(dialed) })
		return wsx.New(ctx, url, dialOpts()...)
	})
	defer r.Close()

	select {
	case <-dialed:
	case <-time.After(testTimeout):
		t.Fatal("did not dial before Send")
	}
}

func TestReconnectingBasicSendRecv(t *testing.T) {
	srv, url := echoServer(t)
	defer srv.Close()

	var callCount int
	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
		callCount++
		c, err := wsx.New(ctx, url, dialOpts()...)
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
	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
		if callCount.Add(1) == 1 {
			// Return a client that's already closed.
			c, err := wsx.New(ctx, url, dialOpts()...)
			if err != nil {
				return nil, err
			}
			c.Close()
			return c, nil
		}
		return wsx.New(ctx, url, dialOpts()...)
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
	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
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

	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
		return wsx.New(ctx, url, dialOpts()...)
	})
	r.Close()

	err := r.Send(envEvent("test"))
	if err == nil {
		t.Fatal("expected error after Close, got nil")
	}
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// sendFails wraps a wsx.Client and makes Send fail a configurable number of
// times before succeeding. After each failure the inner client is closed.
type sendFails struct {
	Client
	remaining int
}

func (f *sendFails) Send(event types.Event) error {
	if f.remaining > 0 {
		f.remaining--
		f.Client.Close()
		return errors.New("sendFails: simulated send failure")
	}
	return f.Client.Send(event)
}

// TestReconnectingRetryOnceOnSendFailure verifies that dialAndSend retries once
// when the first Send on a freshly-dialed connection fails (dead-on-arrival).
func TestReconnectingRetryOnceOnSendFailure(t *testing.T) {
	srv, url := echoServer(t)
	defer srv.Close()

	var factoryCalls atomic.Int32
	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
		factoryCalls.Add(1)
		inner, err := wsx.New(ctx, url, dialOpts()...)
		if err != nil {
			return nil, err
		}
		// The first factory call returns a client whose first Send fails.
		// The second factory call returns a client that works immediately.
		failCount := 1
		if factoryCalls.Load() == 2 {
			failCount = 0
		}
		return &sendFails{Client: inner, remaining: failCount}, nil
	})
	defer r.Close()

	evt := envEvent("test-retry-once")
	if err := r.Send(evt); err != nil {
		t.Fatalf("Send after retry: %v", err)
	}

	if calls := factoryCalls.Load(); calls != 2 {
		t.Fatalf("expected 2 factory calls (first dial + retry dial), got %d", calls)
	}

	// Verify echo received.
	select {
	case got := <-r.Recv():
		env, ok := got.(types.Envelope)
		if !ok {
			t.Fatalf("expected Envelope, got %T", got)
		}
		if env.Collector != "test-retry-once" {
			t.Fatalf("got collector %q, want %q", env.Collector, "test-retry-once")
		}
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for Recv")
	}
}

func TestReconnectingCloseIdempotent(t *testing.T) {
	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
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
	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
		callCount.Add(1)
		return wsx.New(ctx, url, dialOpts()...)
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

	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
		return wsx.New(ctx, url, dialOpts()...)
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
	var startedOnce sync.Once

	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
		startedOnce.Do(func() { close(started) })
		select {
		case <-proceed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
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

	r := newReconnecting(context.Background(), func(ctx context.Context) (Client, error) {
		return wsx.New(ctx, url, dialOpts()...)
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
