package web_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	authsvc "tracer/apps/panel/internal/services/auth"
	ingestsvc "tracer/apps/panel/internal/services/ingest"
	notifysvc "tracer/apps/panel/internal/services/notify"
	panelsvc "tracer/apps/panel/internal/services/panel"
	topologysvc "tracer/apps/panel/internal/services/topology"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/testutil"
	"tracer/apps/panel/web"
)

// newServer builds the full web server stack (services, mux, auth middleware,
// read-only middleware) exactly as production does, registers an admin user
// and a collector API key, and returns the handler and a valid session token.
func newServer(t *testing.T, readOnly bool) (http.Handler, string) {
	t.Helper()

	panelHandle := testutil.NewTestDB(t)

	// Low bcrypt cost: cost 12 is ~250ms per hash, too slow for the suite.
	authService := authsvc.New(panelHandle, testutil.TestKeySecret(), bcrypt.MinCost)
	userService := usersvc.NewService(panelHandle)

	user, err := authService.Register(t.Context(), authsvc.RegisterRequest{
		Username: "alice",
		Password: "secret",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}
	token, err := authService.CreateSession(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, _, err := authService.GenerateCollectorAPIKey(t.Context(), authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	dataDir := t.TempDir()
	dbMgr, err := dbmanager.New(dataDir)
	if err != nil {
		t.Fatalf("create db manager: %v", err)
	}
	t.Cleanup(func() {
		if err := dbMgr.Close(); err != nil {
			t.Errorf("close db manager: %v", err)
		}
	})

	cfg := config.New(config.Panel{
		Port:          "8080",
		DataDir:       dataDir,
		Host:          "https://tracer.example.com",
		SampleRate:    0.1,
		RetentionDays: 90,
	}, filepath.Join(dataDir, "config.yaml"))

	notifyChan := make(chan notifysvc.Notification, 100)
	notifyService := notifysvc.New(panelHandle, userService, map[string]notifysvc.FromRawJSON{})
	ingestService := ingestsvc.New(dbMgr, notifyChan, func(_, _ string, _ int64) string { return "" }, cfg)
	tracker := ingestsvc.NewTracker()
	panelService := panelsvc.New(dbMgr, ingestService, tracker, userService)
	topologyService := topologysvc.NewService(panelService)

	srv := web.NewServer(web.Dependencies{
		DBManagerx:   dbMgr,
		Ingest:       ingestService,
		Notify:       notifyService,
		Auth:         authService,
		User:         userService,
		Tracker:      tracker,
		Topology:     topologyService,
		PanelService: panelService,
		Config:       cfg,
	}, "8080", false, readOnly)

	return srv.Handler, token
}

func TestReadOnlyServerBlocksKeyReveal(t *testing.T) {
	handler, token := newServer(t, true)

	// The detail page is a GET and must still render, masked, without leaking
	// the plaintext — and without the reveal button.
	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/prod-a", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for detail page in read-only mode, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "****") {
		t.Fatalf("expected masked suffix on detail page, got %q", body)
	}
	if strings.Contains(body, "/reveal") {
		t.Fatal("expected no reveal button in read-only mode")
	}

	// Reveal is POST — the read-only middleware must reject it with 405 before
	// any handler runs, even with a valid admin session.
	req = httptest.NewRequest(http.MethodPost, "/settings/admin/collector-api-keys/prod-a/reveal", strings.NewReader("revealed=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for reveal POST in read-only mode, got %d", rr.Code)
	}
}

func TestReadWriteServerAllowsKeyReveal(t *testing.T) {
	handler, token := newServer(t, false)

	// In normal mode the same POST reveal returns the plaintext through the
	// full stack (auth middleware + settings handler).
	req := httptest.NewRequest(http.MethodPost, "/settings/admin/collector-api-keys/prod-a/reveal", strings.NewReader("revealed=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for reveal POST in normal mode, got %d", rr.Code)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", rr.Header().Get("Cache-Control"))
	}
}
