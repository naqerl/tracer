package panel

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/testutil"
)

// TestGetInstallScriptRejectsCraftedCollectorName proves the collector name in
// the path cannot carry shell code into the generated script: it must match the
// identifier rules collector names are created under.
func TestGetInstallScriptRejectsCraftedCollectorName(t *testing.T) {
	mux, apiKey := installScriptMux(t)

	cases := []struct {
		label string
		name  string
	}{
		{label: "command substitution", name: "prod$(touch pwned)"},
		{label: "backticks", name: "prod`touch pwned`"},
		{label: "double quote", name: `prod"x"`},
		{label: "newline", name: "prod\ntouch pwned"},
		{label: "space", name: "prod x"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			rr := installScriptRequest(mux, tc.name, apiKey)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for collector name %q, got %d (body: %s)", tc.name, rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), "#!/bin/bash") {
				t.Errorf("expected no script for collector name %q, got:\n%s", tc.name, rr.Body.String())
			}
		})
	}
}

// TestGetInstallScriptServesScriptForValidCollectorName keeps the ordinary
// install flow working for a name that passes the identifier rules.
func TestGetInstallScriptServesScriptForValidCollectorName(t *testing.T) {
	mux, apiKey := installScriptMux(t)

	rr := installScriptRequest(mux, "prod-a", apiKey)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "TRACER_COLLECTOR_NAME='prod-a'") {
		t.Errorf("expected script for prod-a, got:\n%s", rr.Body.String())
	}
	if !strings.Contains(rr.Header().Get("Content-Disposition"), `filename="install-prod-a.sh"`) {
		t.Errorf("unexpected content disposition %q", rr.Header().Get("Content-Disposition"))
	}
}

// installScriptMux builds the panel mux with a valid collector API key, the
// credential the install-script endpoint authenticates with. The endpoint
// authenticates through the auth service alone, so the panel service is nil.
func installScriptMux(t *testing.T) (*http.ServeMux, string) {
	t.Helper()

	db := testutil.NewTestDB(t)
	authService := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	key, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "install"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}
	if key.Plaintext == "" {
		t.Fatal("generated collector api key has no plaintext")
	}

	return InitMux(nil, authService), key.Plaintext
}

// installScriptRequest asks the install-script endpoint for a collector name,
// percent-encoded the way a client puts it on the wire.
func installScriptRequest(mux *http.ServeMux, collector, apiKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/collectors/"+url.PathEscape(collector)+"/install-script", nil)
	req.Header.Set("X-API-Key", apiKey)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}
