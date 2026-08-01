package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadOnlyMiddlewareAllowsGET(t *testing.T) {
	mw := BuildReadOnlyMiddleware()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/default", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for GET, got %d", rec.Code)
	}
}

func TestReadOnlyMiddlewareRejectsNonGET(t *testing.T) {
	mw := BuildReadOnlyMiddleware()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("handler must not run for %s in read-only mode", r.Method)
	}))
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req := httptest.NewRequest(method, "/settings/admin/collector-api-keys/default/reveal", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405 for %s, got %d", method, rec.Code)
		}
	}
}
