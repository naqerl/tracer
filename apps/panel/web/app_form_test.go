package web_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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
	if !strings.Contains(rr.Body.String(), `App "combo" saved`) {
		t.Fatalf("expected save confirmation, got %s", rr.Body.String())
	}

	app := storedApp(t, panelService, "combo")
	if app.Source == nil || app.Source.Type != "journalctl" || app.Source.Params["unit"] != "combo.service" {
		t.Fatalf("source = %+v, want journalctl combo.service", app.Source)
	}
	if app.Health == nil || app.Health.Params["url"] != "https://example.com/health" {
		t.Fatalf("health = %+v, want example.com/health", app.Health)
	}
	if app.ParserID == "" {
		t.Fatal("expected parser id")
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
