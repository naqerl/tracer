package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	authsvc "tracer/apps/panel/internal/services/auth"
	ingestsvc "tracer/apps/panel/internal/services/ingest"
	panelsvc "tracer/apps/panel/internal/services/panel"
	"tracer/libs/types"
)

func jsonSourceForm(name, unit, healthURL string) url.Values {
	v := url.Values{
		"name":                          {name},
		"service":                       {unit},
		"source_type":                   {"journalctl"},
		"source_params":                 {"unit: " + unit},
		"parser_type":                   {"json"},
		"parser_param_level_field":      {"level"},
		"parser_param_message_field":    {"msg"},
		"parser_param_timestamp_field":  {"time"},
		"parser_param_timestamp_format": {"2006-01-02T15:04:05.999999999Z07:00"},
		"interval":                      {"30s"},
		"expected_status":               {"200"},
	}
	if healthURL != "" {
		v.Set("url", healthURL)
	}
	return v
}

func postApp(t *testing.T, handler http.Handler, token string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/panel/collectors/demo/apps", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func putApp(t *testing.T, handler http.Handler, token, app string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/panel/collectors/demo/apps/"+app, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func storedApp(t *testing.T, svc *panelsvc.Service, name string) types.AppSpec {
	t.Helper()
	raw, err := svc.FetchCollectorConfig(t.Context(), "demo")
	if err != nil {
		t.Fatalf("FetchCollectorConfig: %v", err)
	}
	var cfg types.CollectorConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal config %q: %v", raw, err)
	}
	app, ok := cfg.GetApp(name)
	if !ok {
		t.Fatalf("app %q not in config %s", name, raw)
	}
	return app
}

func TestCreateAppSavesLogsAndHealth(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, jsonSourceForm("combo", "combo.service", "https://example.com/health"))
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Redirect"); got != "/panel" {
		t.Fatalf("HX-Redirect = %q, want /panel", got)
	}

	app := storedApp(t, panelService, "combo")
	if app.Source == nil || app.Source.Type != "journalctl" || app.Source.Params["unit"] != "combo.service" {
		t.Fatalf("source = %+v, want journalctl combo.service", app.Source)
	}
	if app.Health == nil || app.Health.Params["url"] != "https://example.com/health" {
		t.Fatalf("health = %+v, want example.com/health", app.Health)
	}
	if app.Parser == nil {
		t.Fatal("expected inline parser")
	}
}

func TestCreateAppSavesHealthOnly(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, url.Values{
		"name":            {"uptime-only"},
		"url":             {"https://status.example.com"},
		"interval":        {"60s"},
		"expected_status": {"204"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}

	app := storedApp(t, panelService, "uptime-only")
	if !app.IsHealthOnly() {
		t.Fatalf("expected health-only app, got %+v", app)
	}
	if app.Health.Params["url"] != "https://status.example.com" || app.Health.Params["interval"] != "60s" || app.Health.Params["expected_status"] != "204" {
		t.Fatalf("health params = %v", app.Health.Params)
	}
}

func TestAppsGridShowsLogsNotConfiguredForHealthOnlyApp(t *testing.T) {
	handler, token, _, _, _, _ := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, url.Values{
		"name":            {"uptime-only"},
		"url":             {"https://status.example.com"},
		"interval":        {"60s"},
		"expected_status": {"200"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}

	grid := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps", nil)
	grid.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	gridRR := httptest.NewRecorder()
	handler.ServeHTTP(gridRR, grid)
	if gridRR.Code != http.StatusOK {
		t.Fatalf("expected 200 for apps grid, got %d", gridRR.Code)
	}
	body := gridRR.Body.String()

	if !strings.Contains(body, "uptime-only") {
		t.Fatalf("expected health-only app card in grid, got %q", body)
	}
	if !strings.Contains(body, "logs are not configured") {
		t.Fatalf("expected 'logs are not configured' on health-only card, got %q", body)
	}

	// The seeded myapp with a log must keep its error count instead of the
	// not-configured hint.
	if !strings.Contains(body, `<span class="text-fg-secondary">errors</span> <span class="text-fg">1</span>`) {
		t.Fatalf("expected logged app to show its error count, got %q", body)
	}
}

func TestCreateAppSavesLogsOnly(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, jsonSourceForm("logs-only", "logs-only.service", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}

	app := storedApp(t, panelService, "logs-only")
	if app.Source == nil || app.Source.Type != "journalctl" {
		t.Fatalf("source = %+v", app.Source)
	}
	if app.Health != nil {
		t.Fatalf("expected no health, got %+v", app.Health)
	}
}

func TestUpdateAppAddsHealthWithoutDroppingSource(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	create := postApp(t, handler, token, jsonSourceForm("keep-logs", "keep-logs.service", ""))
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", create.Code, create.Body.String())
	}

	rr := putApp(t, handler, token, "keep-logs", jsonSourceForm("keep-logs", "keep-logs.service", "https://keep.example.com/health"))
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d body %s", rr.Code, rr.Body.String())
	}

	app := storedApp(t, panelService, "keep-logs")
	if app.Source == nil || app.Source.Type != "journalctl" || app.Source.Params["unit"] != "keep-logs.service" {
		t.Fatalf("source dropped on health add: %+v", app.Source)
	}
	if app.Health == nil || app.Health.Params["url"] != "https://keep.example.com/health" {
		t.Fatalf("health = %+v", app.Health)
	}
}

func TestUpdateAppEmptyURLClearsHealthKeepsSource(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	create := postApp(t, handler, token, jsonSourceForm("strip-health", "strip-health.service", "https://strip.example.com"))
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", create.Code, create.Body.String())
	}

	form := jsonSourceForm("strip-health", "strip-health.service", "")
	rr := putApp(t, handler, token, "strip-health", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d body %s", rr.Code, rr.Body.String())
	}

	app := storedApp(t, panelService, "strip-health")
	if app.Source == nil {
		t.Fatal("expected source to remain after clearing health")
	}
	if app.Health != nil {
		t.Fatalf("expected health cleared, got %+v", app.Health)
	}
}

func TestUpdateAppURLOnlyDoesNotDropExistingSource(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	create := postApp(t, handler, token, jsonSourceForm("url-only-put", "url-only-put.service", ""))
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", create.Code, create.Body.String())
	}

	// Incomplete source fields (the previous exclusive-url path): a health URL
	// with no parser type must still keep the existing journalctl source.
	rr := putApp(t, handler, token, "url-only-put", url.Values{
		"source_type":     {"journalctl"},
		"parser_type":     {""},
		"url":             {"https://url-only.example.com/health"},
		"interval":        {"30s"},
		"expected_status": {"200"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d body %s", rr.Code, rr.Body.String())
	}

	app := storedApp(t, panelService, "url-only-put")
	if app.Source == nil || app.Source.Type != "journalctl" {
		t.Fatalf("source dropped by url-only PUT: %+v", app)
	}
	if app.Health == nil || app.Health.Params["url"] != "https://url-only.example.com/health" {
		t.Fatalf("health = %+v", app.Health)
	}
}

func TestRenameAppRedirectsAndMigratesData(t *testing.T) {
	handler, token, _, ingestService, _, panelService := newServerWithGuesser(t, nil)

	create := postApp(t, handler, token, jsonSourceForm("renameme", "renameme.service", ""))
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", create.Code, create.Body.String())
	}

	if _, err := ingestService.StoreLog(t.Context(), ingestsvc.StoreLogRequest{
		CollectorName: "demo",
		AppName:       "renameme",
		Entry:         types.LogEntry{Level: types.LevelError, Message: "pre-rename", Timestamp: time.Now()},
	}); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	form := jsonSourceForm("renamednew", "renamednew.service", "https://new.example.com/health")
	rr := putApp(t, handler, token, "renameme", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("rename status = %d body %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Redirect"); got != "/panel/collectors/demo/apps/renamednew" {
		t.Fatalf("HX-Redirect = %q, want /panel/collectors/demo/apps/renamednew", got)
	}

	raw, err := panelService.FetchCollectorConfig(t.Context(), "demo")
	if err != nil {
		t.Fatalf("fetch config: %v", err)
	}
	var cfg types.CollectorConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if _, ok := cfg.GetApp("renameme"); ok {
		t.Fatal("old app name must not remain in config")
	}
	newApp, ok := cfg.GetApp("renamednew")
	if !ok {
		t.Fatal("new app name must exist in config")
	}
	if newApp.Source == nil || newApp.Source.Params["unit"] != "renamednew.service" {
		t.Fatalf("source not migrated: %+v", newApp.Source)
	}
	if newApp.Health == nil || newApp.Health.Params["url"] != "https://new.example.com/health" {
		t.Fatalf("health not migrated: %+v", newApp.Health)
	}
}

func TestEditFormPrefillsSourceAndHealth(t *testing.T) {
	handler, token, _, ingestService, _, _ := newServerWithGuesser(t, nil)

	create := postApp(t, handler, token, jsonSourceForm("prefill", "prefill.service", "https://prefill.example.com"))
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", create.Code, create.Body.String())
	}
	if _, err := ingestService.StoreLog(t.Context(), ingestsvc.StoreLogRequest{
		CollectorName: "demo",
		AppName:       "prefill",
		Entry: types.LogEntry{
			Level:     types.LevelInfo,
			Message:   "seed",
			Timestamp: time.Now(),
		},
	}); err != nil {
		t.Fatalf("seed app db: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/prefill/settings", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("settings status = %d body %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `value="journalctl"`) {
		t.Fatalf("expected journalctl source on edit form, got %s", body)
	}
	if !strings.Contains(body, `value="https://prefill.example.com"`) {
		t.Fatalf("expected health URL on edit form, got %s", body)
	}
}

// TestEditFormPreviewKeepsEditMode verifies that a format guess on the edit
// page re-renders the form as an edit form (PUT to the app URL) and not a
// create form (POST to the apps collection). The guess response replaces the
// form in place, and losing IsEdit there would silently switch the save
// button to create mode.
func TestEditFormPreviewKeepsEditMode(t *testing.T) {
	handler, token, _, _, _, _ := newServerWithGuesser(t, nil)

	create := postApp(t, handler, token, jsonSourceForm("editkeep", "editkeep.service", ""))
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", create.Code, create.Body.String())
	}

	req := httptest.NewRequest(http.MethodPost, "/panel/collectors/demo/apps/preview", strings.NewReader(url.Values{
		"name":        {"editkeep"},
		"service":     {"editkeep.service"},
		"edit_app":    {"editkeep"},
		"source_type": {"journalctl"},
		"parser_type": {"json"},
	}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview status = %d body %s", rr.Code, rr.Body.String())
	}

	body := rr.Body.String()
	if !strings.Contains(body, `hx-put="/panel/collectors/demo/apps/editkeep"`) {
		t.Fatalf("expected edit form to keep hx-put to the app URL, got %s", body)
	}
	if strings.Contains(body, `hx-post="/panel/collectors/demo/apps"`) {
		t.Fatalf("expected preview not to turn edit form into a create form, got %s", body)
	}
	if strings.Contains(body, `hx-put="/panel/collectors/demo/apps/editkeep.service"`) {
		t.Fatalf("edit form must target the app name, not the service name: %s", body)
	}
}

// TestUpdateAppShowsErrorOnRenameConflict verifies that a rejected rename (a
// 4xx-style failure) surfaces as an error message on the page: the response
// re-renders the edit form with the message inline so the HTMX swap keeps the
// form visible and the failed rename is not applied.
func TestUpdateAppShowsErrorOnRenameConflict(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	if rr := postApp(t, handler, token, jsonSourceForm("alpha", "alpha.service", "")); rr.Code != http.StatusOK {
		t.Fatalf("create alpha: %d %s", rr.Code, rr.Body.String())
	}
	if rr := postApp(t, handler, token, jsonSourceForm("beta", "beta.service", "")); rr.Code != http.StatusOK {
		t.Fatalf("create beta: %d %s", rr.Code, rr.Body.String())
	}

	rr := putApp(t, handler, token, "alpha", jsonSourceForm("beta", "beta.service", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with rendered error, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "app name already in use") {
		t.Fatalf("expected conflict message on the page, got %s", body)
	}
	if !strings.Contains(body, `hx-put="/panel/collectors/demo/apps/alpha"`) {
		t.Fatalf("expected the edit form to remain, got %s", body)
	}

	app := storedApp(t, panelService, "alpha")
	if app.Name != "alpha" {
		t.Fatalf("failed rename must not change the app name, got %q", app.Name)
	}
	beta := storedApp(t, panelService, "beta")
	if beta.Name != "beta" {
		t.Fatalf("beta must still exist untouched, got %q", beta.Name)
	}
}

// TestUpdateAppShowsErrorOnSaveFailure verifies that a rejected save (a
// 5xx-style failure) surfaces as an error message on the page: clearing both
// the log source and the health check of an uptime-only app leaves nothing to
// do, the stored-config validation rejects it, and the edit form re-renders
// with the failure inline.
func TestUpdateAppShowsErrorOnSaveFailure(t *testing.T) {
	handler, token, _, _, _, _ := newServerWithGuesser(t, nil)

	if rr := postApp(t, handler, token, url.Values{
		"name":            {"uptime-only"},
		"url":             {"https://status.example.com"},
		"interval":        {"60s"},
		"expected_status": {"200"},
	}); rr.Code != http.StatusOK {
		t.Fatalf("create uptime-only: %d %s", rr.Code, rr.Body.String())
	}

	rr := putApp(t, handler, token, "uptime-only", url.Values{
		"url":             {""},
		"interval":        {""},
		"expected_status": {""},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with rendered error, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Failed to save config") {
		t.Fatalf("expected failure message on the page, got %s", body)
	}
	if !strings.Contains(body, `hx-put="/panel/collectors/demo/apps/uptime-only"`) {
		t.Fatalf("expected the edit form to remain, got %s", body)
	}
}

// TestCreateAppPageIsTheOnlyCreationPage verifies the app creation form lives
// on its own page per collector, that the "+" link points there, and that the
// panel page no longer renders the form itself.
func TestCreateAppPageIsTheOnlyCreationPage(t *testing.T) {
	handler, token, _, _, _, _ := newServerWithGuesser(t, nil)

	plain := httptest.NewRequest(http.MethodGet, "/panel/", nil)
	plain.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	plainRR := httptest.NewRecorder()
	handler.ServeHTTP(plainRR, plain)
	if strings.Contains(plainRR.Body.String(), `hx-post="/panel/collectors/demo/apps"`) {
		t.Fatalf("expected no create form on the panel page, got %s", plainRR.Body.String())
	}
	if !strings.Contains(plainRR.Body.String(), `href="/panel/collectors/demo/app"`) {
		t.Fatalf("expected the add link to point at the creation page, got %s", plainRR.Body.String())
	}

	withParam := httptest.NewRequest(http.MethodGet, "/panel/?add=demo", nil)
	withParam.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	paramRR := httptest.NewRecorder()
	handler.ServeHTTP(paramRR, withParam)
	if strings.Contains(paramRR.Body.String(), `hx-post="/panel/collectors/demo/apps"`) {
		t.Fatalf("expected no create form for the add parameter, got %s", paramRR.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/app", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("creation page status = %d body %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `hx-post="/panel/collectors/demo/apps"`) {
		t.Fatalf("expected the create form on the creation page, got %s", body)
	}
	if !strings.Contains(body, "Add app to demo") {
		t.Fatalf("expected the create form to name its collector, got %s", body)
	}
	assertCancelAnchor(t, body, "/panel")

	form := appFormFragment(t, body)
	if strings.Contains(form, "onclick=") {
		t.Fatalf("expected no inline JavaScript in the form, got %s", form)
	}
	if strings.Contains(form, "use app name") {
		t.Fatalf("expected the use-app-name button to be gone, got %s", form)
	}
	if strings.Contains(form, "app-form.js") {
		t.Fatalf("expected no app-form script, got %s", form)
	}
}

// TestCreateAppPageRefusesUserWithoutModifyAccess verifies that the creation
// page is not served to a user who may not add apps: the route answers 403 and
// renders no form, so nothing points them at a page that only refuses them.
func TestCreateAppPageRefusesUserWithoutModifyAccess(t *testing.T) {
	handler, _, _, _, authService, _ := newServerWithGuesser(t, nil)
	viewer := registerViewer(t, authService)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/app", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: viewer})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a user without modify access, got %d body %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `hx-post="/panel/collectors/demo/apps"`) {
		t.Fatalf("expected no create form for a forbidden user, got %s", rr.Body.String())
	}
}

// TestCreateAppPageUnknownCollector verifies the creation page answers 404 for
// a collector that does not exist.
func TestCreateAppPageUnknownCollector(t *testing.T) {
	handler, token, _, _, _, _ := newServerWithGuesser(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/ghost/app", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown collector, got %d body %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), `hx-post="/panel/collectors/ghost/apps"`) {
		t.Fatalf("expected no create form for an unknown collector, got %s", rr.Body.String())
	}
}

// TestCollectorOverviewLinksToAppCreationPage verifies the collector overview
// carries the add-app link to the creation page.
func TestCollectorOverviewLinksToAppCreationPage(t *testing.T) {
	handler, token, _, _, _, _ := newServerWithGuesser(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("collector overview status = %d body %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `href="/panel/collectors/demo/app"`) {
		t.Fatalf("expected the add link on the collector overview, got %s", rr.Body.String())
	}
}

// TestCreateAppRejectsUnusableHealthURL verifies a health URL that is not an
// http(s) address is refused with the reason on the re-rendered form and is
// not stored, since nothing downstream would notice it until the monitor
// reported the app as down.
func TestCreateAppRejectsUnusableHealthURL(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	for _, tc := range []struct {
		rawURL string
		want   string
	}{
		{"ftp://example.com", "health check url must be http or https"},
		{"example.com/health", "health check url must be http or https"},
		{"http://", "health check url must name a host"},
		{"https://exa mple.com", "could not parse health check url"},
	} {
		rr := postApp(t, handler, token, url.Values{
			"name": {"bad-url"},
			"url":  {tc.rawURL},
		})
		if rr.Code != http.StatusOK {
			t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), tc.want) {
			t.Fatalf("expected %q on the form for %q, got %s", tc.want, tc.rawURL, rr.Body.String())
		}
		if got := rr.Header().Get("HX-Redirect"); got != "" {
			t.Fatalf("expected no redirect for %q, got %q", tc.rawURL, got)
		}
	}

	raw, err := panelService.FetchCollectorConfig(t.Context(), "demo")
	if err != nil {
		t.Fatalf("fetch config: %v", err)
	}
	if raw != "" {
		var cfg types.CollectorConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatalf("unmarshal config %q: %v", raw, err)
		}
		if _, ok := cfg.GetApp("bad-url"); ok {
			t.Fatalf("expected no app stored for an unusable health URL, got %s", raw)
		}
	}
}

// TestAppFormCancelHrefFollowsReRenders verifies the form's Cancel destination
// survives every re-render that swaps the form in place. The log source input
// and the save error both replace the whole form, so a Cancel that loses its
// destination there leaves the user on a form they cannot leave.
func TestAppFormCancelHrefFollowsReRenders(t *testing.T) {
	handler, token, _, _, _, _ := newServerWithGuesser(t, nil)

	preview := postForm(t, handler, token, "/panel/collectors/demo/apps/preview", url.Values{
		"name":    {"cancelcheck"},
		"service": {"cancelcheck.service"},
	})
	if preview.Code != http.StatusOK {
		t.Fatalf("preview status = %d body %s", preview.Code, preview.Body.String())
	}
	assertCancelAnchor(t, preview.Body.String(), "/panel")

	invalid := postApp(t, handler, token, url.Values{"name": {""}})
	if invalid.Code != http.StatusOK {
		t.Fatalf("invalid create status = %d body %s", invalid.Code, invalid.Body.String())
	}
	if !strings.Contains(invalid.Body.String(), "app name is required") {
		t.Fatalf("expected the create error on the re-rendered form, got %s", invalid.Body.String())
	}
	assertCancelAnchor(t, invalid.Body.String(), "/panel")

	if rr := postApp(t, handler, token, jsonSourceForm("cancelalpha", "cancelalpha.service", "")); rr.Code != http.StatusOK {
		t.Fatalf("create cancelalpha: %d %s", rr.Code, rr.Body.String())
	}
	if rr := postApp(t, handler, token, jsonSourceForm("cancelbeta", "cancelbeta.service", "")); rr.Code != http.StatusOK {
		t.Fatalf("create cancelbeta: %d %s", rr.Code, rr.Body.String())
	}
	conflict := putApp(t, handler, token, "cancelalpha", jsonSourceForm("cancelbeta", "cancelbeta.service", ""))
	if conflict.Code != http.StatusOK {
		t.Fatalf("conflicting rename status = %d body %s", conflict.Code, conflict.Body.String())
	}
	if !strings.Contains(conflict.Body.String(), "app name already in use") {
		t.Fatalf("expected the rename error on the re-rendered form, got %s", conflict.Body.String())
	}
	assertCancelAnchor(t, conflict.Body.String(), "/panel/collectors/demo")
}

// TestNewAppPageIsGone verifies the old /panel/collectors/{collector}/apps/new
// route no longer serves the add form: the path is now an ordinary app URL, so
// it must not render a create form.
func TestNewAppPageIsGone(t *testing.T) {
	handler, token, _, _, _, _ := newServerWithGuesser(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/new", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if strings.Contains(rr.Body.String(), `hx-post="/panel/collectors/demo/apps"`) {
		t.Fatalf("expected no add form on the removed route, got %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "Add app") {
		t.Fatalf("expected no add-app page, got %s", rr.Body.String())
	}
}

// registerViewer registers a non-admin user with no app access and returns a
// session token for them.
func registerViewer(t *testing.T, authService *authsvc.Service) string {
	t.Helper()
	user, err := authService.Register(t.Context(), authsvc.RegisterRequest{
		Username: "viewer",
		Password: "secret",
		IsAdmin:  false,
	})
	if err != nil {
		t.Fatalf("register viewer: %v", err)
	}
	token, err := authService.CreateSession(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("create viewer session: %v", err)
	}
	return token
}

func postForm(t *testing.T, handler http.Handler, token, path string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func assertCancelAnchor(t *testing.T, body, want string) {
	t.Helper()
	if !strings.Contains(body, `<a href="`+want+`"`) {
		t.Fatalf("expected the Cancel anchor to point at %q, got %s", want, body)
	}
	if strings.Contains(body, `<a href=""`) {
		t.Fatalf("Cancel anchor has no destination: %s", body)
	}
}

// appFormFragment returns the rendered app form, without the page chrome that
// surrounds it.
func appFormFragment(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<div id="app-form-container"`)
	if start < 0 {
		t.Fatalf("no app form in page: %s", body)
	}
	end := strings.Index(body[start:], `</form>`)
	if end < 0 {
		t.Fatalf("unterminated app form: %s", body)
	}
	return body[start : start+end]
}

func TestCreateAppSavesRetrySettings(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, url.Values{
		"name":            {"slow-origin"},
		"url":             {"https://slow.example.com/health"},
		"interval":        {"30s"},
		"expected_status": {"200"},
		"timeout":         {"15s"},
		"attempts":        {"1"},
		"retry_interval":  {"0s"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}

	app := storedApp(t, panelService, "slow-origin")
	if app.Health == nil {
		t.Fatal("expected a health check")
	}
	if app.Health.Params["timeout"] != "15s" || app.Health.Params["attempts"] != "1" || app.Health.Params["retry_interval"] != "0s" {
		t.Fatalf("health params = %v, want the submitted retry settings", app.Health.Params)
	}
}

func TestCreateAppRejectsOutOfRangeTimeout(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, url.Values{
		"name":    {"too-slow"},
		"url":     {"https://example.com/health"},
		"timeout": {"90s"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "invalid uptime check") || !strings.Contains(body, "timeout") {
		t.Fatalf("expected the timeout reason on the form, got %s", body)
	}
	if !strings.Contains(body, `name="timeout" value="90s"`) {
		t.Fatalf("expected the form to keep the submitted timeout, got %s", body)
	}

	raw, err := panelService.FetchCollectorConfig(t.Context(), "demo")
	if err != nil {
		t.Fatalf("FetchCollectorConfig: %v", err)
	}
	if strings.Contains(raw, "too-slow") {
		t.Fatalf("invalid check was stored: %s", raw)
	}
}

func TestCreateAppRejectsRetryIntervalLongerThanDefaultTimeout(t *testing.T) {
	handler, token, _, _, _, _ := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, url.Values{
		"name":           {"slow-retry"},
		"url":            {"https://example.com/health"},
		"retry_interval": {"10s"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "shorter than timeout 5s") {
		t.Fatalf("expected the retry interval reason on the form, got %s", rr.Body.String())
	}
}

func TestUpdateAppClearsRetrySettings(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	create := postApp(t, handler, token, url.Values{
		"name":            {"clear-timeout"},
		"url":             {"https://clear.example.com/health"},
		"interval":        {"30s"},
		"expected_status": {"200"},
		"timeout":         {"15s"},
		"attempts":        {"1"},
		"retry_interval":  {"0s"},
	})
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", create.Code, create.Body.String())
	}

	rr := putApp(t, handler, token, "clear-timeout", url.Values{
		"name":            {"clear-timeout"},
		"url":             {"https://clear.example.com/health"},
		"interval":        {"30s"},
		"expected_status": {"200"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d body %s", rr.Code, rr.Body.String())
	}

	app := storedApp(t, panelService, "clear-timeout")
	for _, key := range []string{"timeout", "attempts", "retry_interval"} {
		if _, ok := app.Health.Params[key]; ok {
			t.Fatalf("cleared %s was stored, params = %v", key, app.Health.Params)
		}
	}
}

func TestUpdateAppKeepsLegacyHealthParamsUnchanged(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	form := url.Values{
		"name":            {"legacy-check"},
		"url":             {"https://legacy.example.com/health"},
		"interval":        {"60s"},
		"expected_status": {"204"},
		"alert_delay":     {"5m"},
	}
	if rr := postApp(t, handler, token, form); rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}
	before := storedApp(t, panelService, "legacy-check").Health.Params

	rr := putApp(t, handler, token, "legacy-check", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d body %s", rr.Code, rr.Body.String())
	}
	after := storedApp(t, panelService, "legacy-check").Health.Params

	if len(before) != len(after) {
		t.Fatalf("params = %v, want the original %v", after, before)
	}
	for key, want := range before {
		if after[key] != want {
			t.Fatalf("params = %v, want the original %v", after, before)
		}
	}
}

func TestEditFormPrefillsRetrySettingsAndAppPageSummarizes(t *testing.T) {
	handler, token, _, ingestService, _, _ := newServerWithGuesser(t, nil)

	create := postApp(t, handler, token, url.Values{
		"name":            {"summary-check"},
		"url":             {"https://summary.example.com/health"},
		"interval":        {"30s"},
		"expected_status": {"200"},
		"timeout":         {"15s"},
	})
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", create.Code, create.Body.String())
	}
	if _, err := ingestService.StoreLog(t.Context(), ingestsvc.StoreLogRequest{
		CollectorName: "demo",
		AppName:       "summary-check",
		Entry:         types.LogEntry{Level: types.LevelInfo, Message: "seed", Timestamp: time.Now()},
	}); err != nil {
		t.Fatalf("seed app db: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/summary-check/settings", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("settings status = %d body %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `name="timeout" value="15s"`) {
		t.Fatalf("expected the stored timeout on the edit form, got %s", body)
	}
	if !strings.Contains(body, `name="attempts" value="" placeholder="3"`) {
		t.Fatalf("expected an empty attempts field with the default placeholder, got %s", body)
	}
	if !strings.Contains(body, `name="timeout" value="15s" placeholder="5s"`) {
		t.Fatalf("expected the timeout placeholder to stay 5s, got %s", body)
	}

	page := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/summary-check?level=error", nil)
	page.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	pageRR := httptest.NewRecorder()
	handler.ServeHTTP(pageRR, page)
	if pageRR.Code != http.StatusOK {
		t.Fatalf("app page status = %d body %s", pageRR.Code, pageRR.Body.String())
	}
	if !strings.Contains(pageRR.Body.String(), "HTTP · every 30s · timeout 15s · 3 attempts · retry 1s") {
		t.Fatalf("expected the health summary on the app page, got %s", pageRR.Body.String())
	}
}

func TestAppPageSummaryUsesDefaultTimeoutAfterClearing(t *testing.T) {
	handler, token, _, ingestService, _, _ := newServerWithGuesser(t, nil)

	create := postApp(t, handler, token, url.Values{
		"name":            {"default-check"},
		"url":             {"https://default.example.com/health"},
		"interval":        {"30s"},
		"expected_status": {"200"},
		"timeout":         {"15s"},
	})
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", create.Code, create.Body.String())
	}
	if _, err := ingestService.StoreLog(t.Context(), ingestsvc.StoreLogRequest{
		CollectorName: "demo",
		AppName:       "default-check",
		Entry:         types.LogEntry{Level: types.LevelInfo, Message: "seed", Timestamp: time.Now()},
	}); err != nil {
		t.Fatalf("seed app db: %v", err)
	}

	if rr := putApp(t, handler, token, "default-check", url.Values{
		"name":            {"default-check"},
		"url":             {"https://default.example.com/health"},
		"interval":        {"30s"},
		"expected_status": {"200"},
	}); rr.Code != http.StatusOK {
		t.Fatalf("update status = %d body %s", rr.Code, rr.Body.String())
	}

	page := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/default-check?level=error", nil)
	page.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	pageRR := httptest.NewRecorder()
	handler.ServeHTTP(pageRR, page)
	if pageRR.Code != http.StatusOK {
		t.Fatalf("app page status = %d body %s", pageRR.Code, pageRR.Body.String())
	}
	if !strings.Contains(pageRR.Body.String(), "timeout 5s") {
		t.Fatalf("expected the default timeout in the summary, got %s", pageRR.Body.String())
	}
}
