package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCreateCommandHealthApp(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, url.Values{
		"name":          {"pg-replica"},
		"health_type":   {"command"},
		"command":       {"pg_isready -h 127.0.0.1 -p 5433"},
		"expect":        {"accepting connections"},
		"expected_exit": {"0,2"},
		"interval":      {"60s"},
		"alert_delay":   {"5m"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Redirect"); got != "/panel" {
		t.Fatalf("HX-Redirect = %q, want /panel", got)
	}

	app := storedApp(t, panelService, "pg-replica")
	if app.Health == nil || app.Health.Type != "command" {
		t.Fatalf("health = %+v, want a command check", app.Health)
	}
	params := app.Health.Params
	if params["command"] != "pg_isready -h 127.0.0.1 -p 5433" ||
		params["expect"] != "accepting connections" ||
		params["expected_exit"] != "0,2" ||
		params["interval"] != "60s" ||
		params["alert_delay"] != "5m" {
		t.Fatalf("health params = %v", params)
	}
}

func TestCreateCommandHealthRequiresCommand(t *testing.T) {
	handler, token, _, _, _, _ := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, url.Values{
		"name":        {"pg-mcp"},
		"health_type": {"command"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid uptime check") {
		t.Fatalf("expected the check reason on the form, got %s", rr.Body.String())
	}
}

func TestCreateCommandHealthRejectsInvalidExpect(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, url.Values{
		"name":        {"pg-bad-regexp"},
		"health_type": {"command"},
		"command":     {"pg_isready"},
		"expect":      {"("},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "invalid uptime check") || !strings.Contains(body, "expect regexp") {
		t.Fatalf("expected an actionable invalid-expect error, got %s", body)
	}

	raw, err := panelService.FetchCollectorConfig(t.Context(), "demo")
	if err != nil {
		t.Fatalf("FetchCollectorConfig: %v", err)
	}
	if strings.Contains(raw, "pg-bad-regexp") {
		t.Fatalf("invalid command check was stored: %s", raw)
	}
}

func TestUpdateCommandHealthKeepsCommand(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	create := postApp(t, handler, token, url.Values{
		"name":        {"pg-mcp"},
		"health_type": {"command"},
		"command":     {"pg_isready"},
	})
	if create.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", create.Code, create.Body.String())
	}

	form := url.Values{
		"name":          {"pg-mcp"},
		"health_type":   {"command"},
		"command":       {"pg_isready -h 127.0.0.1"},
		"expect":        {"ready"},
		"expected_exit": {"0,1"},
		"interval":      {"60s"},
		"alert_delay":   {"1m"},
	}
	rr := putApp(t, handler, token, "pg-mcp", form)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d body %s", rr.Code, rr.Body.String())
	}

	app := storedApp(t, panelService, "pg-mcp")
	if app.Health == nil || app.Health.Type != "command" || app.Health.Params["command"] != "pg_isready -h 127.0.0.1" {
		t.Fatalf("health = %+v, want the updated command", app.Health)
	}
	if app.Health.Params["expected_exit"] != "0,1" || app.Health.Params["alert_delay"] != "1m" {
		t.Fatalf("health params = %v", app.Health.Params)
	}
}

func TestCreateCommandHealthIgnoresRetrySettings(t *testing.T) {
	handler, token, _, _, _, panelService := newServerWithGuesser(t, nil)

	rr := postApp(t, handler, token, url.Values{
		"name":           {"pg-timeout"},
		"health_type":    {"command"},
		"command":        {"pg_isready"},
		"timeout":        {"30s"},
		"attempts":       {"2"},
		"retry_interval": {"5s"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("create status = %d body %s", rr.Code, rr.Body.String())
	}

	app := storedApp(t, panelService, "pg-timeout")
	if app.Health == nil || app.Health.Params["timeout"] != "30s" {
		t.Fatalf("health = %+v, want a 30s timeout", app.Health)
	}
	for _, key := range []string{"attempts", "retry_interval"} {
		if _, ok := app.Health.Params[key]; ok {
			t.Fatalf("command check stored %s, params = %v", key, app.Health.Params)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/panel/collectors/demo/apps/pg-timeout/settings", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	settings := httptest.NewRecorder()
	handler.ServeHTTP(settings, req)
	if settings.Code != http.StatusOK {
		t.Fatalf("settings status = %d body %s", settings.Code, settings.Body.String())
	}
	body := settings.Body.String()
	if strings.Contains(body, `name="attempts"`) || strings.Contains(body, `name="retry_interval"`) {
		t.Fatalf("expected retry fields to be hidden for a command check, got %s", body)
	}
	if !strings.Contains(body, `name="timeout"`) {
		t.Fatalf("expected the timeout field for a command check, got %s", body)
	}
}
