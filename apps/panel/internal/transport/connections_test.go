package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/types"
	"tracer/libs/wsx"
)

// echoServer returns a test server that echoes every message it receives,
// so a push from the registry comes back to the client's Recv channel.
func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
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
	return srv
}

func TestConnectionsPushDeliversToConnectedCollector(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	cl, err := wsx.New(context.Background(), url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()

	reg := NewConnections()
	reg.Add("c1", cl)
	reg.PushCollectorConfig("c1", "apps:\n  - name: x\n")

	select {
	case evt := <-cl.Recv():
		ce, ok := evt.(types.ConfigEvent)
		if !ok {
			t.Fatalf("expected ConfigEvent, got %T", evt)
		}
		if ce.Config != "apps:\n  - name: x\n" {
			t.Fatalf("config mismatch: %q", ce.Config)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for pushed config")
	}
}

func TestConnectionsRemoveStopsPush(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")

	cl, err := wsx.New(context.Background(), url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()

	reg := NewConnections()
	reg.Add("c1", cl)
	reg.Remove("c1", cl)
	reg.PushCollectorConfig("c1", "x")

	select {
	case <-cl.Recv():
		t.Fatal("received config after removal")
	case <-time.After(100 * time.Millisecond):
	}
}
