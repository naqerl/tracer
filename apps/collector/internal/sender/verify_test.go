package sender

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestVerifySuccess checks that a reachable panel with a valid API key passes.
func TestVerifySuccess(t *testing.T) {
	srv, url := startVerifyServer(t, http.StatusSwitchingProtocols, "")
	defer srv.Close()

	spec := Spec{Name: "test-collector", PanelHost: strings.TrimPrefix(url, "ws://"), APIKey: "valid-key"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if err := Verify(ctx, spec, []string{"app1"}); err != nil {
		t.Fatalf("Verify() error = %v, want nil", err)
	}
}

// TestVerifyUnauthorized checks that a 401 response is reported as a bad API key.
func TestVerifyUnauthorized(t *testing.T) {
	srv, url := startVerifyServer(t, http.StatusUnauthorized, "Invalid API key")
	defer srv.Close()

	spec := Spec{Name: "test-collector", PanelHost: strings.TrimPrefix(url, "ws://"), APIKey: "wrong-key"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := Verify(ctx, spec, nil)
	if err == nil {
		t.Fatal("Verify() error = nil, want API key rejection")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("Verify() error = %q, want it to mention HTTP 401", err)
	}
}

// TestVerifyBadHandshake checks that a non-401 handshake rejection is reported
// as a wrong panel host.
func TestVerifyBadHandshake(t *testing.T) {
	srv, url := startVerifyServer(t, http.StatusNotFound, "not found")
	defer srv.Close()

	spec := Spec{Name: "test-collector", PanelHost: strings.TrimPrefix(url, "ws://"), APIKey: "any-key"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := Verify(ctx, spec, nil)
	if err == nil {
		t.Fatal("Verify() error = nil, want handshake rejection")
	}
	if !strings.Contains(err.Error(), "handshake") {
		t.Fatalf("Verify() error = %q, want it to mention the handshake", err)
	}
}

// TestVerifyMissingAPIKey checks that an empty API key fails fast with guidance.
func TestVerifyMissingAPIKey(t *testing.T) {
	spec := Spec{Name: "test-collector", PanelHost: "localhost:8080", APIKey: ""}
	if err := Verify(t.Context(), spec, nil); err == nil {
		t.Fatal("Verify() error = nil, want missing api_key error")
	}
}

// TestVerifyMissingPanelHost checks that an empty panel host fails fast.
func TestVerifyMissingPanelHost(t *testing.T) {
	spec := Spec{Name: "test-collector", PanelHost: "", APIKey: "key"}
	if err := Verify(t.Context(), spec, nil); err == nil {
		t.Fatal("Verify() error = nil, want missing panel_host error")
	}
}

// TestVerifyConnectionRefused checks that an unreachable panel is reported.
func TestVerifyConnectionRefused(t *testing.T) {
	spec := Spec{Name: "test-collector", PanelHost: "127.0.0.1:1", APIKey: "key"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := Verify(ctx, spec, nil)
	if err == nil {
		t.Fatal("Verify() error = nil, want connection error")
	}
}

// TestVerifyPanelSendsMessage checks that a panel sending a message and then
// staying open still counts as a successful verification.
func TestVerifyPanelSendsMessage(t *testing.T) {
	srv := startVerifyServerWithBehavior(t, func(conn *websocket.Conn) {
		// Read the registration, send an arbitrary message, then keep the
		// connection open until the client hangs up.
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"ping"}`)); err != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	defer srv.Close()

	spec := Spec{Name: "test-collector", PanelHost: strings.TrimPrefix(srv.URL, "http://"), APIKey: "key"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if err := Verify(ctx, spec, nil); err != nil {
		t.Fatalf("Verify() error = %v, want nil", err)
	}
}

// TestVerifyPanelClosesAfterRegistration checks that a panel closing the
// connection right after registration is reported as a failure.
func TestVerifyPanelClosesAfterRegistration(t *testing.T) {
	srv := startVerifyServerWithBehavior(t, func(conn *websocket.Conn) {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = conn.Close()
	})
	defer srv.Close()

	spec := Spec{Name: "test-collector", PanelHost: strings.TrimPrefix(srv.URL, "http://"), APIKey: "key"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	err := Verify(ctx, spec, nil)
	if err == nil {
		t.Fatal("Verify() error = nil, want panel-closed error")
	}
	if !strings.Contains(err.Error(), "could not keep the connection open") {
		t.Fatalf("Verify() error = %q, want it to mention the connection being closed", err)
	}
}

// startVerifyServer starts a fake panel that either upgrades the WebSocket
// connection (status 101) or rejects it with the given status and body.
func startVerifyServer(t *testing.T, status int, body string) (*httptest.Server, string) {
	t.Helper()

	if status != http.StatusSwitchingProtocols {
		return startVerifyServerRejecting(t, status, body)
	}

	srv := startVerifyServerWithBehavior(t, func(conn *websocket.Conn) {
		// Hold the connection open until the client hangs up.
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	return srv, "ws://" + strings.TrimPrefix(srv.URL, "http://")
}

// startVerifyServerRejecting starts a fake panel that answers every request
// with the given HTTP status and body.
func startVerifyServerRejecting(t *testing.T, status int, body string) (*httptest.Server, string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, body, status)
	}))

	url := "ws://" + strings.TrimPrefix(srv.URL, "http://")
	return srv, url
}

// startVerifyServerWithBehavior starts a fake panel that upgrades the
// WebSocket connection and hands it to the given behavior function, which
// runs in a goroutine per connection.
func startVerifyServerWithBehavior(t *testing.T, behavior func(conn *websocket.Conn)) *httptest.Server {
	t.Helper()

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("test server upgrade error: %v", err)
			return
		}
		defer conn.Close()
		behavior(conn)
	}))
}
