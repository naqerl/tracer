package web_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	"tracer/libs/types"
)

// newServer builds the full web server stack (services, mux, auth middleware,
// read-only middleware) exactly as production does, registers an admin user
// and a collector API key, and returns the handler, a valid session token, and
// the ID of a log group seeded under the demo collector's "myapp" app.
func newServer(t *testing.T, readOnly bool) (http.Handler, string, int64) {
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
	if _, err := dbMgr.CreateCollectorDB("demo"); err != nil {
		t.Fatalf("create demo collector db: %v", err)
	}
	if _, err := dbMgr.CreateAppDB("demo", "myapp"); err != nil {
		t.Fatalf("create demo app db: %v", err)
	}

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

	// Seed one error log group via the public ingest API, then resolve its
	// group ID through the public panel API.
	if _, err := ingestService.StoreLog(t.Context(), ingestsvc.StoreLogRequest{
		CollectorName: "demo",
		AppName:       "myapp",
		Entry: types.LogEntry{
			Level:     types.LevelError,
			Message:   "demo group",
			Timestamp: time.Now(),
		},
	}); err != nil {
		t.Fatalf("seed demo log: %v", err)
	}
	var groupID int64
	groups, err := panelService.GetLogGroups(t.Context(), panelsvc.GetLogGroupsRequest{
		UserID:        user.ID,
		CollectorName: "demo",
		AppName:       "myapp",
		Level:         "error",
		Since:         time.Now().Add(-time.Hour),
		Limit:         50,
	})
	if err != nil {
		t.Fatalf("list demo log groups: %v", err)
	}
	for _, g := range groups {
		if g.Message == "demo group" {
			groupID = g.ID
			break
		}
	}
	if groupID == 0 {
		t.Fatalf("seeded demo log group not found")
	}

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

	return srv.Handler, token, groupID
}

func TestReadOnlyServerBlocksKeyReveal(t *testing.T) {
	handler, token, _ := newServer(t, true)

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
	handler, token, _ := newServer(t, false)

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

func TestAutoRefreshDashboardRendersPollingTriggers(t *testing.T) {
	handler, token, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for dashboard, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `hx-trigger="load, every 15s"`) {
		t.Fatal("expected dashboard to emit the 15s polling trigger")
	}
	if !strings.Contains(body, "/panel/collectors/demo/stats") {
		t.Fatal("expected dashboard stats row fragment URL")
	}
	if !strings.Contains(body, "/panel/collectors/demo/apps") {
		t.Fatal("expected dashboard apps grid fragment URL")
	}
	if strings.Count(body, `hx-indicator="#apps-loading-64656d6f"`) < 2 {
		t.Fatal("expected both dashboard polling sections (stats + apps) to reference the per-collector indicator")
	}
	if !strings.Contains(body, `id="apps-loading-64656d6f"`) || !strings.Contains(body, "Refreshing…") {
		t.Fatal("expected dashboard collector title to carry the refresh indicator")
	}
	if !strings.Contains(body, "animate-pulse") {
		t.Fatal("expected dashboard to render app skeleton cards")
	}
	if strings.Contains(body, "Loading apps...") {
		t.Fatal("expected the app skeleton to replace the loading indicator")
	}
}

func TestAutoRefreshAppsGridBakesStats(t *testing.T) {
	handler, token, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for apps grid fragment, got %d", rr.Code)
	}
	body := rr.Body.String()

	if !strings.Contains(body, "myapp") {
		t.Fatalf("expected seeded app in grid, got %q", body)
	}
	if !strings.Contains(body, "uptime:") || !strings.Contains(body, "errors:") {
		t.Fatalf("expected baked uptime + errors in grid, got %q", body)
	}
	if !strings.Contains(body, ">1</span>") {
		t.Fatalf("expected baked error count of 1, got %q", body)
	}
	// The seeded error count must be color-coded red.
	if !strings.Contains(body, "errors: <span class=\"inline-block w-[3ch] tabular-nums text-left whitespace-nowrap text-red-400\">1</span>") {
		t.Fatalf("expected red error count in grid, got %q", body)
	}

	if strings.Contains(body, "uptime/percentage") || strings.Contains(body, "errors/count") {
		t.Fatalf("expected no per-app hx-get in grid, got %q", body)
	}
}

func TestAutoRefreshCollectorOverviewRendersPollingTriggers(t *testing.T) {
	handler, token, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for collector overview, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "/panel/collectors/demo/stats") {
		t.Fatal("expected overview stats row fragment URL")
	}
	if !strings.Contains(body, "/panel/collectors/demo/apps") {
		t.Fatal("expected overview apps grid fragment URL")
	}
	// The overview uses a single shared indicator id for both polling sections.
	if strings.Count(body, `hx-indicator="#apps-loading"`) < 2 {
		t.Fatal("expected both overview polling sections (stats + apps) to reference the shared indicator")
	}
	if !strings.Contains(body, `id="apps-loading"`) || !strings.Contains(body, "Refreshing…") {
		t.Fatal("expected overview header to carry the refresh indicator")
	}
	if !strings.Contains(body, "apps-grid") || !strings.Contains(body, "animate-pulse") {
		t.Fatal("expected overview to render the apps skeleton")
	}
}

func TestAutoRefreshCollectorStatsFragment(t *testing.T) {
	handler, token, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/stats", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for collector stats fragment, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "No stats available") {
		t.Fatalf("expected stats fragment body, got %q", rr.Body.String())
	}
}

func TestAutoRefreshCollectorStatsFragmentNotFound(t *testing.T) {
	handler, token, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/ghost/stats", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown collector stats, got %d", rr.Code)
	}
}

func TestAutoRefreshAppUptimeFragmentNotFound(t *testing.T) {
	handler, token, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/ghost/uptime", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown app uptime, got %d", rr.Code)
	}
}

func TestAutoRefreshLogGroupSummaryFragmentNotFound(t *testing.T) {
	handler, token, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/ghost/logs/groups/1/summary", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown app summary, got %d", rr.Code)
	}
}

func TestAutoRefreshAppPageRendersPollingTriggers(t *testing.T) {
	handler, token, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/myapp?level=error", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for app page, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `hx-trigger="load, every 15s"`) {
		t.Fatal("expected app page to emit the 15s polling trigger")
	}
	if !strings.Contains(body, "/panel/collectors/demo/apps/myapp/uptime") {
		t.Fatal("expected app page uptime fragment URL")
	}
	if !strings.Contains(body, "/panel/collectors/demo/apps/myapp/stats") {
		t.Fatal("expected app page stats fragment URL")
	}
	if !strings.Contains(body, "/panel/collectors/demo/apps/myapp/logs/groups?level=error") {
		t.Fatal("expected app page log groups fragment URL")
	}
}

func TestAutoRefreshUptimeFragmentSuccess(t *testing.T) {
	handler, token, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/myapp/uptime", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for uptime fragment, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Uptime") {
		t.Fatalf("expected uptime panel body, got %q", rr.Body.String())
	}
}

func TestAutoRefreshLogGroupsFragmentSelfPoll(t *testing.T) {
	handler, token, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/myapp/logs/groups?level=error", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for log groups fragment, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `id="log-groups"`) {
		t.Fatal("expected log groups fragment root id")
	}
	if !strings.Contains(body, `hx-trigger="every 15s"`) {
		t.Fatal("expected log groups fragment to self-poll every 15s")
	}
	if !strings.Contains(body, `hx-swap="outerHTML"`) {
		t.Fatal("expected log groups fragment to self-swap outerHTML")
	}
	if !strings.Contains(body, "level=error") {
		t.Fatal("expected log groups fragment hx-get to carry the current level")
	}
	if !strings.Contains(body, `hx-sync="#log-groups:replace"`) {
		t.Fatal("expected filter links to sync with the polling root")
	}
	if !strings.Contains(body, "demo group") {
		t.Fatalf("expected seeded log group in fragment body, got %q", body)
	}
}

func TestAutoRefreshLogGroupSummaryFragmentSuccess(t *testing.T) {
	handler, token, groupID := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/panel/collectors/demo/apps/myapp/logs/groups/%d/summary", groupID), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for log group summary fragment, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Count:") || !strings.Contains(body, "error") {
		t.Fatalf("expected summary metadata body, got %q", body)
	}
	if !strings.Contains(body, `name="since"`) {
		t.Fatalf("expected since input in summary fragment, got %q", body)
	}
}

func TestAutoRefreshLogGroupSummaryNewLogs(t *testing.T) {
	handler, token, groupID := newServer(t, false)

	// A since timestamp in the past (before the seeded log) must surface the
	// "+N" indicator.
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/panel/collectors/demo/apps/myapp/logs/groups/%d/summary?since=%d", groupID, time.Now().Add(-time.Hour).Unix()), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for log group summary fragment, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "+1") {
		t.Fatalf("expected +1 new logs indicator, got %q", body)
	}
	if !strings.Contains(body, "There are new log records, refresh the page to see them") {
		t.Fatalf("expected new-logs tooltip text, got %q", body)
	}

	// A since timestamp in the future (after the seeded log) must show no
	// indicator.
	req = httptest.NewRequest(http.MethodGet, fmt.Sprintf("/panel/collectors/demo/apps/myapp/logs/groups/%d/summary?since=%d", groupID, time.Now().Add(time.Hour).Unix()), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for log group summary fragment, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "+1") {
		t.Fatalf("expected no +N indicator for future since, got %q", rr.Body.String())
	}
}
