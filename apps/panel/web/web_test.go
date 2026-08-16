package web_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
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
func newServer(t *testing.T, readOnly bool) (http.Handler, string, int64, *ingestsvc.Service, *authsvc.Service) {
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
	if _, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
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

	return srv.Handler, token, groupID, ingestService, authService
}

// collectorKeyID resolves the id of the collector API key with the given name
// through the public list API.
func collectorKeyID(t *testing.T, authService *authsvc.Service, name string) int64 {
	t.Helper()
	keys, err := authService.ListCollectorAPIKeys(t.Context(), domain.System{})
	if err != nil {
		t.Fatalf("list collector api keys: %v", err)
	}
	for _, k := range keys {
		if k.Name == name {
			return k.ID
		}
	}
	t.Fatalf("collector api key %q not found", name)
	return 0
}

func TestReadOnlyServerBlocksKeyReveal(t *testing.T) {
	handler, token, _, _, authService := newServer(t, true)

	// The detail page is a GET and must still render, masked, without leaking
	// the plaintext — and without the reveal button.
	keyID := collectorKeyID(t, authService, "prod-a")
	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/"+strconv.FormatInt(keyID, 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for detail page in read-only mode, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "•") {
		t.Fatalf("expected masked placeholder on detail page, got %q", body)
	}
	if strings.Contains(body, "/reveal") {
		t.Fatal("expected no reveal button in read-only mode")
	}

	// Reveal is POST — the read-only middleware must reject it with 405 before
	// any handler runs, even with a valid admin session.
	req = httptest.NewRequest(http.MethodPost, "/settings/admin/collector-api-keys/"+strconv.FormatInt(keyID, 10)+"/reveal", strings.NewReader("revealed=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405 for reveal POST in read-only mode, got %d", rr.Code)
	}
}

func TestReadWriteServerAllowsKeyReveal(t *testing.T) {
	handler, token, _, _, authService := newServer(t, false)

	// In normal mode the same POST reveal returns the plaintext through the
	// full stack (auth middleware + settings handler).
	keyID := collectorKeyID(t, authService, "prod-a")
	req := httptest.NewRequest(http.MethodPost, "/settings/admin/collector-api-keys/"+strconv.FormatInt(keyID, 10)+"/reveal", strings.NewReader("revealed=1"))
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
	handler, token, _, _, _ := newServer(t, false)

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
	if strings.Contains(body, "hx-indicator") || strings.Contains(body, "Refreshing") {
		t.Fatal("expected indicators to be removed from the dashboard")
	}
	if !strings.Contains(body, "animate-pulse") {
		t.Fatal("expected dashboard to render app skeleton cards")
	}
	if strings.Contains(body, "Loading apps...") {
		t.Fatal("expected the app skeleton to replace the loading indicator")
	}
}

func TestAutoRefreshAppsGridBakesStats(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

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
	// The seeded error count must be color-coded red and carry the breath
	// animation (it refreshes every 15s).
	if !strings.Contains(body, "errors: <span class=\"inline-block w-[3ch] tabular-nums text-left whitespace-nowrap value-breath text-red-400\">1</span>") {
		t.Fatalf("expected red breathing error count in grid, got %q", body)
	}

	if strings.Contains(body, "uptime/percentage") || strings.Contains(body, "errors/count") {
		t.Fatalf("expected no per-app hx-get in grid, got %q", body)
	}
}

func TestAutoRefreshCollectorOverviewRendersPollingTriggers(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

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
	if strings.Contains(body, "hx-indicator") || strings.Contains(body, "Refreshing") {
		t.Fatal("expected indicators to be removed from the collector overview")
	}
	if !strings.Contains(body, "apps-grid") || !strings.Contains(body, "animate-pulse") {
		t.Fatal("expected overview to render the apps skeleton")
	}
}

func TestAutoRefreshCollectorStatsFragment(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

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

func TestAutoRefreshDashboardBreathesValuesOnly(t *testing.T) {
	// Seed real stats through the public ingest API so the stats row renders
	// actual CPU/RAM/Disk values instead of the "no stats" fallback.
	handler, token, _, ingestService, _ := newServer(t, false)
	if err := ingestService.SaveSystemStats(t.Context(), ingestsvc.SaveSystemStatsRequest{
		CollectorName: "demo",
		Stats: types.Stats{
			CPUUsagePercent: 42,
			MemTotalMB:      8192,
			MemUsedMB:       4096,
			DiskTotalGB:     100,
			DiskUsedGB:      50,
			Timestamp:       time.Now(),
		},
	}); err != nil {
		t.Fatalf("seed demo stats: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for dashboard, got %d", rr.Code)
	}
	body := rr.Body.String()
	// The three resource values carry value-breath; their labels do not.
	for _, v := range []string{"CPU:", "RAM:", "Disk:"} {
		if !strings.Contains(body, "<span class=\"text-fg-secondary\">"+v+"</span>") {
			t.Fatalf("expected static label %q with no animation class, got %q", v, body)
		}
	}
	if strings.Count(body, "value-breath") != 3 {
		t.Fatalf("expected exactly 3 breathing values (CPU/RAM/Disk), got %d occurrences", strings.Count(body, "value-breath"))
	}
}

func TestAutoRefreshAppStatsFragmentBreathesValuesOnly(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/myapp/stats", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for app stats fragment, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Total Logs") || !strings.Contains(body, "Errors") {
		t.Fatalf("expected app stats fragment body, got %q", body)
	}
	if strings.Count(body, "value-breath") != 4 {
		t.Fatalf("expected the four stat counts to carry value-breath, got %d occurrences", strings.Count(body, "value-breath"))
	}
	// The static labels must not animate.
	for _, label := range []string{"Total Logs", "Errors", "Warnings", "Info"} {
		if strings.Contains(body, label+" value-breath") || strings.Contains(body, "value-breath "+label) {
			t.Fatalf("expected static label %q to stay unanimated, got %q", label, body)
		}
	}
}

func TestAutoRefreshUptimeFragmentBreathesValue(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/myapp/uptime", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for uptime fragment, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `<h2 class="text-lg font-medium mb-4">Uptime <span class="value-breath">`) {
		t.Fatalf("expected the uptime value (not the label) to carry value-breath, got %q", body)
	}
}

func TestAutoRefreshCollectorStatsFragmentNotFound(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/ghost/stats", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown collector stats, got %d", rr.Code)
	}
}

func TestAutoRefreshAppUptimeFragmentNotFound(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/ghost/uptime", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown app uptime, got %d", rr.Code)
	}
}

func TestAutoRefreshLogGroupSummaryFragmentNotFound(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/ghost/logs/groups/1/summary", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown app summary, got %d", rr.Code)
	}
}

func TestAutoRefreshAppPageRendersPollingTriggers(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

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
	handler, token, _, _, _ := newServer(t, false)

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
	handler, token, _, _, _ := newServer(t, false)

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
	handler, token, groupID, _, _ := newServer(t, false)

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
	handler, token, groupID, _, _ := newServer(t, false)

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

func TestMarkdownAcceptDashboard(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	// Plain HTML (default Accept) — untouched.
	req := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for dashboard, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("expected text/html for normal request, got %q", ct)
	}
	if !strings.Contains(rr.Body.String(), "demo") {
		t.Fatalf("expected dashboard HTML to contain demo collector, got %q", rr.Body.String())
	}

	// Accept: text/markdown — dashboard converts.
	req = httptest.NewRequest(http.MethodGet, "/panel/", nil)
	req.Header.Set("Accept", "text/markdown")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for dashboard markdown, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("expected text/markdown content type, got %q", ct)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, "<div") {
		t.Fatalf("expected converted markdown without HTML tags, got:\n%s", body)
	}
	if !strings.Contains(body, "demo") {
		t.Fatalf("expected converted markdown to contain demo collector, got:\n%s", body)
	}
	// The apps grid is hx-get loaded; the anchor conversion must turn the
	// placeholder into a link so the route appears in the markdown.
	if !strings.Contains(body, "[collectors/demo/apps](/panel/collectors/demo/apps)") {
		t.Fatalf("expected apps-grid anchor in converted markdown, got:\n%s", body)
	}
	if rr.Header().Get("Content-Length") != "" {
		t.Fatal("expected Content-Length removed after conversion")
	}
}

func TestMarkdownAcceptSSEUnchanged(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	// The topology SSE stream is infinite; cancel the request context so the
	// handler returns after the first flush. Content-Type must stay
	// text/event-stream (not converted, not buffered forever).
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/topology/events", nil)
	req.Header.Set("Accept", "text/markdown")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream for topology SSE, got %q", ct)
	}
}

// TestMarkdownAcceptCollectorPage verifies the collector overview page
// (GET /panel/collectors/{collector}) converts to markdown with the seeded
// collector name and stats area present, and stays HTML without the header.
func TestMarkdownAcceptCollectorPage(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	// Plain HTML (default Accept) — untouched.
	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for collector page, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("expected text/html for normal request, got %q", ct)
	}
	if !strings.Contains(rr.Body.String(), "demo") {
		t.Fatalf("expected collector HTML to contain demo collector, got %q", rr.Body.String())
	}

	// Accept: text/markdown — collector page converts.
	req = httptest.NewRequest(http.MethodGet, "/panel/collectors/demo", nil)
	req.Header.Set("Accept", "text/markdown")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for collector markdown, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("expected text/markdown content type, got %q", ct)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, "<div") {
		t.Fatalf("expected converted markdown without HTML tags, got:\n%s", body)
	}
	if !strings.Contains(body, "demo") {
		t.Fatalf("expected converted markdown to contain demo collector, got:\n%s", body)
	}
	// The stats area renders its empty state (the seeded collector has no
	// stats) plus the Metrics link; both must survive conversion.
	if !strings.Contains(body, "No stats available") || !strings.Contains(body, "Metrics") {
		t.Fatalf("expected stats labels in converted markdown, got:\n%s", body)
	}
	// The apps grid is hx-get loaded; the anchor conversion turns the
	// placeholder into a link so the route appears in the markdown.
	if !strings.Contains(body, "[collectors/demo/apps](/panel/collectors/demo/apps)") {
		t.Fatalf("expected apps-grid anchor in converted markdown, got:\n%s", body)
	}
}

// TestMarkdownAcceptAppPage verifies the app page
// (GET /panel/collectors/{collector}/apps/{app}?level=error) converts to
// markdown with the seeded app name and uptime heading present, and stays
// HTML without the header.
func TestMarkdownAcceptAppPage(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	// Plain HTML (default Accept) — untouched.
	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/myapp?level=error", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for app page, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("expected text/html for normal request, got %q", ct)
	}
	if !strings.Contains(rr.Body.String(), "myapp") {
		t.Fatalf("expected app HTML to contain myapp, got %q", rr.Body.String())
	}

	// Accept: text/markdown — app page converts.
	req = httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/myapp?level=error", nil)
	req.Header.Set("Accept", "text/markdown")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for app markdown, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("expected text/markdown content type, got %q", ct)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, "<div") {
		t.Fatalf("expected converted markdown without HTML tags, got:\n%s", body)
	}
	if !strings.Contains(body, "myapp") {
		t.Fatalf("expected converted markdown to contain myapp, got:\n%s", body)
	}
	if !strings.Contains(body, "Uptime") {
		t.Fatalf("expected uptime heading in converted markdown, got:\n%s", body)
	}
	// The log groups div is hx-get loaded on mount; the anchor conversion
	// turns it into a link so the log groups route appears in the markdown.
	if !strings.Contains(body, "[collectors/demo/apps/myapp/logs/groups?level=error](/panel/collectors/demo/apps/myapp/logs/groups?level=error)") {
		t.Fatalf("expected log groups anchor in converted markdown, got:\n%s", body)
	}
}

// TestMarkdownAcceptLogGroupsFragment verifies the log groups fragment
// (GET /panel/collectors/{collector}/apps/{app}/logs/groups?level=error)
// converts to markdown carrying the seeded "demo group" log message — the
// page data the app page loads via hx-get on mount.
func TestMarkdownAcceptLogGroupsFragment(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/myapp/logs/groups?level=error", nil)
	req.Header.Set("Accept", "text/markdown")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for log groups markdown, got %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("expected text/markdown content type, got %q", ct)
	}
	body := rr.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, "<div") || strings.Contains(body, "<table") {
		t.Fatalf("expected converted markdown without HTML tags, got:\n%s", body)
	}
	if !strings.Contains(body, "Log Groups") {
		t.Fatalf("expected log groups heading in converted markdown, got:\n%s", body)
	}
	if !strings.Contains(body, "demo group") {
		t.Fatalf("expected seeded log group message in converted markdown, got:\n%s", body)
	}
}

// TestMarkdownAcceptNotFoundStaysHTML verifies a missing resource keeps its
// HTML error page rather than being converted to markdown.
func TestMarkdownAcceptNotFoundStaysHTML(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/ghost", nil)
	req.Header.Set("Accept", "text/markdown")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 status preserved, got %d", rr.Code)
	}
	body := rr.Body.String()
	if ct := rr.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("expected error page not to be converted to markdown, got %q", ct)
	}
	if !strings.Contains(body, "Collector not found") {
		t.Fatalf("expected error message in body, got %q", body)
	}
	if !strings.Contains(body, "<html") {
		t.Fatalf("expected HTML error page body, got %q", body)
	}
}

// TestMarkdownAcceptRealServer locks the flush-ordering fix on a real
// net/http server: templ flushes after every render, and forwarding an
// uncommitted flush would commit an implicit empty 200 before the middleware
// acts. A real server exposes the corruption that httptest.NewRecorder masks.
func TestMarkdownAcceptRealServer(t *testing.T) {
	handler, token, _, _, _ := newServer(t, false)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// A converted page must arrive with the markdown content type over the
	// wire (the recorder keeps a live header map, a real server does not).
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/panel/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/markdown")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Fatalf("expected text/markdown over the wire, got %q", ct)
	}

	// A 404 must stay 404 over the wire (the flush bug corrupted it to 200).
	req, err = http.NewRequest(http.MethodGet, srv.URL+"/panel/collectors/ghost", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/markdown")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 over the wire, got %d", resp.StatusCode)
	}
}
