package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	panelsvc "tracer/apps/panel/internal/services/panel"
	"tracer/libs/types"
)

// recordingGuesser captures the service the panel asked the collector to read
// and answers with a fixed guess.
type recordingGuesser struct {
	services []string
	result   types.FormatResultEvent
}

func (g *recordingGuesser) GuessFormat(ctx context.Context, collector string, req types.FormatRequestEvent) (types.FormatResultEvent, error) {
	g.services = append(g.services, req.Service)
	return g.result, nil
}

func guessFor(resource string) types.FormatResultEvent {
	return types.FormatResultEvent{
		App: types.AppSpec{
			Source: &types.SourceSpec{Type: "kubectl", Params: map[string]string{"resource": resource}},
		},
		Parser: types.ParserSpec{
			Type: "slogtext",
			Params: map[string]string{
				"level_field":      "level",
				"message_field":    "msg",
				"timestamp_field":  "time",
				"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
			},
		},
		Samples: []string{`time=2026-10-03T12:00:00Z level=INFO msg=ready`},
	}
}

// TestPreviewWithEmptyLogSourceSkipsTheGuess covers an app that has no log
// source at all: an uptime-only app is valid, so the preview guesses nothing
// and reports nothing instead of naming the empty field as a problem.
func TestPreviewWithEmptyLogSourceSkipsTheGuess(t *testing.T) {
	guesser := &recordingGuesser{result: guessFor("deploy/barse")}
	handler, token, _, _, _, _ := newServerWithGuesser(t, panelsvc.FormatGuesser(guesser))

	req := httptest.NewRequest(http.MethodPost, "/panel/collectors/lab/apps/preview", strings.NewReader(url.Values{}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty fields, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if len(guesser.services) != 0 {
		t.Errorf("expected no guess for an empty log source, got %v", guesser.services)
	}
	if strings.Contains(rr.Body.String(), "Enter a log source") {
		t.Errorf("expected no complaint about an empty log source, got:\n%s", rr.Body.String())
	}
}

// TestCreateAppGuessesTheLogSourceNotTheAppName is the regression this fixes.
// The panel used to guess on the app name and ignore the log source the user
// typed, so a name like "barse-web" reading a workload called "barse" failed
// even though the preview had detected the format correctly.
func TestCreateAppGuessesTheLogSourceNotTheAppName(t *testing.T) {
	guesser := &recordingGuesser{result: guessFor("deploy/barse")}
	handler, token, _, _, _, panelService := newServerWithGuesser(t, panelsvc.FormatGuesser(guesser))

	form := url.Values{
		"name":    {"barse-web"},
		"service": {"deploy/barse"},
	}
	req := httptest.NewRequest(http.MethodPost, "/panel/collectors/lab/apps", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if len(guesser.services) == 0 {
		t.Fatal("expected the collector to be asked for a format guess")
	}
	if guesser.services[0] != "deploy/barse" {
		t.Errorf("guessed %q, want the log source deploy/barse", guesser.services[0])
	}
	if strings.Contains(strings.Join(guesser.services, ","), "barse-web") {
		t.Error("guessed the app name instead of the log source")
	}

	app := storedLabApp(t, panelService, "barse-web")
	if app.Source == nil || app.Source.Type != "kubectl" || app.Source.Params["resource"] != "deploy/barse" {
		t.Fatalf("stored source = %+v, want the guessed kubectl deploy/barse", app.Source)
	}
	if app.Health != nil {
		t.Fatalf("expected no uptime check, got %+v", app.Health)
	}
}

// TestCreateAppWithoutLogSourceDoesNotGuess covers an uptime-only app: nothing
// names a log source, so the collector is never asked to guess one.
func TestCreateAppWithoutLogSourceDoesNotGuess(t *testing.T) {
	guesser := &recordingGuesser{result: guessFor("deploy/barse")}
	handler, token, _, _, _, panelService := newServerWithGuesser(t, panelsvc.FormatGuesser(guesser))

	form := url.Values{
		"name":            {"uptime-only"},
		"url":             {"https://status.example.com"},
		"interval":        {"60s"},
		"expected_status": {"204"},
	}
	req := httptest.NewRequest(http.MethodPost, "/panel/collectors/lab/apps", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if len(guesser.services) != 0 {
		t.Errorf("guessed %q for an app with no log source", guesser.services)
	}

	app := storedLabApp(t, panelService, "uptime-only")
	if !app.IsHealthOnly() {
		t.Fatalf("expected an uptime-only app, got %+v", app)
	}
	if app.Health.Params["url"] != "https://status.example.com" || app.Health.Params["interval"] != "60s" {
		t.Fatalf("health params = %v", app.Health.Params)
	}
}

// TestCreateAppRejectsABadUptimeCheck keeps a rejected check on the form: the
// user sees why instead of an error page replacing the form they were filling.
func TestCreateAppRejectsABadUptimeCheck(t *testing.T) {
	guesser := &recordingGuesser{result: guessFor("deploy/barse")}
	handler, token, _, _, _, _ := newServerWithGuesser(t, panelsvc.FormatGuesser(guesser))

	form := url.Values{
		"name": {"bad-check"},
		"url":  {"ftp://status.example.com"},
	}
	req := httptest.NewRequest(http.MethodPost, "/panel/collectors/lab/apps", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with the form re-rendered, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid uptime check") {
		t.Errorf("expected the reason on the form, got:\n%s", rr.Body.String())
	}
}

// TestCreateAppReportsAFailedGuess covers a log source the collector cannot
// resolve on a form that also carries an uptime check: the reason the collector
// gave has to reach the form, and it must not read as an app with nothing to
// collect.
func TestCreateAppReportsAFailedGuess(t *testing.T) {
	guesser := &recordingGuesser{result: types.FormatResultEvent{
		NotFound: true,
		Error:    `pods "barse" not found`,
	}}
	handler, token, _, _, _, _ := newServerWithGuesser(t, panelsvc.FormatGuesser(guesser))

	form := url.Values{
		"name":    {"barse-web"},
		"service": {"deploy/barse"},
		"url":     {"https://barse.example.com/health"},
	}
	req := httptest.NewRequest(http.MethodPost, "/panel/collectors/lab/apps", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with the form re-rendered, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "log source not found for barse-web") {
		t.Errorf("expected the collector reason on the form, got:\n%s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "pods &#34;barse&#34; not found") {
		t.Errorf("expected the collector's own message on the form, got:\n%s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "nothing to do") {
		t.Errorf("a failed guess must not be reported as an app with nothing to do, got:\n%s", rr.Body.String())
	}
}

// TestCreateAppValidatesTheCheckWhenTheGuessFails pins the order: the check is
// validated on its own, so the user fixes the URL before being sent back to the
// log source the collector could not resolve.
func TestCreateAppValidatesTheCheckWhenTheGuessFails(t *testing.T) {
	guesser := &recordingGuesser{result: types.FormatResultEvent{
		NotFound: true,
		Error:    `pods "barse" not found`,
	}}
	handler, token, _, _, _, _ := newServerWithGuesser(t, panelsvc.FormatGuesser(guesser))

	form := url.Values{
		"name":    {"barse-web"},
		"service": {"deploy/barse"},
		"url":     {"ftp://barse.example.com"},
	}
	req := httptest.NewRequest(http.MethodPost, "/panel/collectors/lab/apps", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with the form re-rendered, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid uptime check") {
		t.Errorf("expected the check reason on the form, got:\n%s", rr.Body.String())
	}
}

// TestEditAppRejectsABadUptimeCheck pins the same validation on the edit path:
// an app stored with a valid check must not be saved with url=ftp://… or an
// interval the collector cannot parse.
func TestEditAppRejectsABadUptimeCheck(t *testing.T) {
	guesser := &recordingGuesser{result: guessFor("deploy/barse")}
	handler, token, _, _, _, panelService := newServerWithGuesser(t, panelsvc.FormatGuesser(guesser))

	create := url.Values{
		"name":    {"barse-web"},
		"service": {"deploy/barse"},
		"url":     {"https://barse.example.com/health"},
	}
	createReq := httptest.NewRequest(http.MethodPost, "/panel/collectors/lab/apps", strings.NewReader(create.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createReq.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	createRR := httptest.NewRecorder()
	handler.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusOK {
		t.Fatalf("create app: %d (body: %s)", createRR.Code, createRR.Body.String())
	}

	form := url.Values{
		"name":     {"barse-web"},
		"url":      {"ftp://barse.example.com"},
		"interval": {"every minute"},
	}
	req := httptest.NewRequest(http.MethodPut, "/panel/collectors/lab/apps/barse-web", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !strings.Contains(rr.Body.String(), "invalid uptime check") {
		t.Errorf("expected the reason on the form, got:\n%s", rr.Body.String())
	}
	app := storedLabApp(t, panelService, "barse-web")
	if app.Health == nil || app.Health.Params["url"] != "https://barse.example.com/health" {
		t.Fatalf("stored health = %+v, want the check saved before the edit", app.Health)
	}
}

// storedLabApp reads back an app of the "lab" collector the tests above create.
func storedLabApp(t *testing.T, svc *panelsvc.Service, name string) types.AppSpec {
	t.Helper()
	raw, err := svc.FetchCollectorConfig(t.Context(), "lab")
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
