package web_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
)

// TestMCPEndpointIsMountedAndGated checks the endpoint through the whole server
// stack: the mount, every method on it, and that the panel's session cookie is
// not a credential for MCP.
func TestMCPEndpointIsMountedAndGated(t *testing.T) {
	handler, session, _, _, authService := newServer(t)
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`

	// Every method reaches the same gate, not just the POST a client normally
	// sends.
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete, http.MethodHead} {
		req := httptest.NewRequest(method, "/mcp", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s /mcp with no key = %d, want 401", method, rec.Code)
		}
	}

	// A sibling path is not the endpoint.
	req := httptest.NewRequest(http.MethodPost, "/panel/mcp", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("POST /panel/mcp = 200, want it not to be the MCP endpoint")
	}

	// A logged-in browser must not be able to drive MCP.
	req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initialize))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: session})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("POST /mcp with only a session cookie = %d, want 401", rec.Code)
	}

	// A user API key does: the keyed initialize speaks MCP.
	userID, _, err := authService.ValidateSession(t.Context(), session)
	if err != nil {
		t.Fatalf("validate session: %v", err)
	}
	key, err := authService.GenerateUserAPIKey(t.Context(), domain.System{}, authsvc.GenerateUserAPIKeyRequest{UserID: userID, Name: "mcp"})
	if err != nil {
		t.Fatalf("generate user api key: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initialize))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-API-Key", key.Plaintext)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /mcp with a user API key = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("content type = %q, want text/event-stream", ct)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"serverInfo"`) {
		t.Errorf("body = %q, want an initialize result", body)
	}

	// The same key ends the session, which also closes the SDK's idle timer.
	sessionID := rec.Header().Get("Mcp-Session-Id")
	if sessionID == "" {
		t.Fatal("the keyed initialize returned no session id")
	}
	req = httptest.NewRequest(http.MethodDelete, "/mcp", nil)
	req.Header.Set("X-API-Key", key.Plaintext)
	req.Header.Set("Mcp-Session-Id", sessionID)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent && rec.Code != http.StatusOK {
		t.Errorf("DELETE /mcp with a key = %d, want the session closed (200 or 204)", rec.Code)
	}
}
