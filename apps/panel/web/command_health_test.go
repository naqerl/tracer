package web_test

import (
	"net/http"
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
