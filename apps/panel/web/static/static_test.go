package static

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStaticAssetsRevalidate asserts that static assets carry a revalidating
// Cache-Control header so browsers never serve stale unversioned JS/CSS after
// an upgrade.
func TestStaticAssetsRevalidate(t *testing.T) {
	mux := InitMux()

	req := httptest.NewRequest(http.MethodGet, "/htmx.min.js", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "public, max-age=0, must-revalidate" {
		t.Errorf("Cache-Control = %q, want %q", got, "public, max-age=0, must-revalidate")
	}
}
