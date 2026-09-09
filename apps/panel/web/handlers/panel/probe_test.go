package panel

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsExpectedHTTPStatus2xxWhenEmpty(t *testing.T) {
	expected := map[int]struct{}{}
	if !isExpectedHTTPStatus(200, expected) {
		t.Fatal("200 should be accepted with empty expected set")
	}
	if isExpectedHTTPStatus(500, expected) {
		t.Fatal("500 should not be accepted with empty expected set")
	}
}

func TestIsExpectedHTTPStatusExplicit(t *testing.T) {
	expected := parseExpectedStatuses("200, 204;301")
	if !isExpectedHTTPStatus(204, expected) {
		t.Fatal("204 should be accepted")
	}
	if isExpectedHTTPStatus(302, expected) {
		t.Fatal("302 should not be accepted")
	}
}

func TestProbeUpAndDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	text, ok := healthProbeText(t.Context(), srv.URL, "200")
	if !ok {
		t.Fatalf("expected up probe, text=%q", text)
	}
	if text == "" {
		t.Fatal("expected non-empty up text")
	}

	text, ok = healthProbeText(t.Context(), srv.URL, "500")
	if ok {
		t.Fatalf("expected down probe, text=%q", text)
	}
}

func TestProbeStatusDiffers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	want := "status differs (got 200, want 201)"
	text, ok := healthProbeText(t.Context(), srv.URL, "201")
	if ok {
		t.Fatalf("expected mismatch, text=%q", text)
	}
	if text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
}

func TestProbeEmptyURLHidden(t *testing.T) {
	text, ok := healthProbeText(t.Context(), "", "200")
	if ok || text != "" {
		t.Fatalf("empty url should be hidden, got ok=%v text=%q", ok, text)
	}
}

func TestProbeUnreachable(t *testing.T) {
	text, ok := healthProbeText(t.Context(), "http://127.0.0.1:1/", "200")
	if ok {
		t.Fatalf("expected unreachable to be down, text=%q", text)
	}
	if text == "" {
		t.Fatal("expected non-empty unreachable text")
	}
}
