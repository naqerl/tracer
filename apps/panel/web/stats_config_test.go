package web_test

import (
	"strings"
	"testing"

	"net/http"
	"net/http/httptest"
)

func TestStatsConfigFormRoundTrip(t *testing.T) {
	handler, token, _, _, _ := newServer(t)

	// GET the stats config page — should render the new override fields.
	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/stats/configure", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET stats config, got %d", rr.Code)
	}
	body := rr.Body.String()
	for _, field := range []string{"cpu_delta_percent", "mem_delta_kb", "disk_delta_kb",
		"cpu_threshold_percent", "mem_threshold_percent", "disk_threshold_percent",
		"resource_notify_minutes"} {
		if !strings.Contains(body, `name="`+field+`"`) {
			t.Errorf("expected form field %q in GET response", field)
		}
	}

	// POST with override values set.
	form := "interval=30s&cpu=on&mem=on&disk=on" +
		"&cpu_delta_percent=0.5&mem_delta_kb=32&disk_delta_kb=512" +
		"&cpu_threshold_percent=85&mem_threshold_percent=75&disk_threshold_percent=88" +
		"&resource_notify_minutes=15"
	req = httptest.NewRequest(http.MethodPost, "/panel/collectors/demo/stats/configure", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for POST stats config, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Metrics config saved") {
		t.Fatalf("expected success message, got %q", rr.Body.String())
	}

	// GET again — values should be pre-filled.
	req = httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/stats/configure", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET after save, got %d", rr.Code)
	}
	body = rr.Body.String()
	for _, want := range []string{`value="0.5"`, `value="32"`, `value="512"`,
		`value="85.0"`, `value="75.0"`, `value="88.0"`, `value="15"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in form after save, not found in body", want)
		}
	}

	// POST with empty overrides — clears to nil (use global defaults).
	form = "interval=30s&cpu=on&mem=on&disk=on"
	req = httptest.NewRequest(http.MethodPost, "/panel/collectors/demo/stats/configure", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for POST with empty overrides, got %d", rr.Code)
	}

	// GET again — values should be empty, placeholders show global defaults.
	req = httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/stats/configure", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET after clearing, got %d", rr.Code)
	}
	body = rr.Body.String()
	// Placeholders should show global defaults.
	if !strings.Contains(body, "global: 1.0") {
		t.Error("expected 'global: 1.0' placeholder for CPU delta")
	}
	if !strings.Contains(body, "global: 64") {
		t.Error("expected 'global: 64' placeholder for Memory delta")
	}
}

func TestStatsConfigInvalidIntervalPreservesFields(t *testing.T) {
	handler, token, _, _, _ := newServer(t)

	form := "interval=invalid&cpu=on&cpu_delta_percent=2.5&mem_threshold_percent=80"
	req := httptest.NewRequest(http.MethodPost, "/panel/collectors/demo/stats/configure", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for invalid interval, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Invalid interval") {
		t.Fatalf("expected 'Invalid interval' error, got %q", body)
	}
	// Override values should be preserved in the error re-render.
	if !strings.Contains(body, `name="cpu_delta_percent"`) {
		t.Fatal("expected form fields preserved in error re-render")
	}
}

func TestStatsConfigFormPlaceholdersShowGlobalDefaults(t *testing.T) {
	handler, token, _, _, _ := newServer(t)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/stats/configure", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	// A fresh collector with no overrides should show all global placeholders.
	for _, ph := range []string{"global: 1.0", "global: 64", "global: 1024", "global: 0", "global: 10"} {
		if !strings.Contains(body, `placeholder="`+ph+`"`) {
			t.Errorf("expected placeholder %q in form", ph)
		}
	}
}
