package panel

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"tracer/libs/types"
)

// TestSaveAppGuessesTheLogSourceNotTheAppName pins what the collector is asked
// to read: the log source the user typed. Guessing on the app name read a
// resource that does not exist whenever the two differ.
func TestSaveAppGuessesTheLogSourceNotTheAppName(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	g := &recordingFormatGuesser{
		byService: map[string]types.FormatResultEvent{
			"deploy/barse": slogtextGuess("deploy/barse"),
		},
	}
	svc.guesser = g

	err := svc.SaveApp(t.Context(), collectorName, NewApp{Name: "barse-web", Service: "deploy/barse"})
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}

	if got := strings.Join(g.services, ","); got != "deploy/barse" {
		t.Fatalf("guessed service = %q, want deploy/barse", got)
	}
	if strings.Contains(strings.Join(g.services, ","), "barse-web") {
		t.Fatal("guessed the app name instead of the log source")
	}
}

// TestSaveAppWithoutLogSourceDoesNotGuess pins that an empty log source sends
// no service at all: uptime-only apps exist, so the app name is never
// substituted for a log source the user did not enter.
func TestSaveAppWithoutLogSourceDoesNotGuess(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	g := &recordingFormatGuesser{}
	svc.guesser = g

	err := svc.SaveApp(t.Context(), collectorName, NewApp{
		Name:     "uptime-only",
		URL:      "https://status.example.com",
		Interval: "60s",
	})
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}

	if len(g.services) != 0 {
		t.Fatalf("guessed %q for an app with no log source", strings.Join(g.services, ","))
	}
	app := savedApp(t, svc, collectorName, "uptime-only")
	if !app.IsHealthOnly() {
		t.Fatalf("expected an uptime-only app, got %+v", app)
	}
}

// TestSaveAppWithoutLogSourceOrCheckIsRejected covers a form with neither a log
// source nor an uptime check: there is nothing for the collector to do.
func TestSaveAppWithoutLogSourceOrCheckIsRejected(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)

	err := svc.SaveApp(t.Context(), collectorName, NewApp{Name: "empty"})
	if !errors.Is(err, ErrAppNoSource) {
		t.Fatalf("SaveApp error = %v, want ErrAppNoSource", err)
	}
}

// TestSaveAppKeepsTheFormSource pins that a source the form spelled out is
// stored as it stands, without asking the collector to guess it again.
func TestSaveAppKeepsTheFormSource(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	g := &recordingFormatGuesser{}
	svc.guesser = g

	err := svc.SaveApp(t.Context(), collectorName, NewApp{
		Name:    "combo",
		Source:  &types.SourceSpec{Type: "journalctl", Params: map[string]string{"unit": "combo.service"}},
		Parser:  types.ParserSpec{Type: "json", Params: map[string]string{"level_field": "level", "message_field": "msg", "timestamp_field": "time", "timestamp_format": time.RFC3339}},
		Service: "ignored-because-source-is-set",
		URL:     "https://combo.example.com/health",
	})
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}

	if len(g.services) != 0 {
		t.Fatalf("guessed %q for an app whose source the form spelled out", strings.Join(g.services, ","))
	}
	app := savedApp(t, svc, collectorName, "combo")
	if app.Source == nil || app.Source.Type != "journalctl" || app.Source.Params["unit"] != "combo.service" {
		t.Fatalf("source = %+v, want journalctl combo.service", app.Source)
	}
	if app.Parser == nil || app.Parser.Type != "json" {
		t.Fatalf("parser = %+v, want json", app.Parser)
	}
	if app.Health == nil || app.Health.Params["url"] != "https://combo.example.com/health" {
		t.Fatalf("health = %+v, want the submitted URL", app.Health)
	}
}

// TestSaveAppRejectsADuplicateName keeps an existing app from being silently
// replaced by a second one under the same name.
func TestSaveAppRejectsADuplicateName(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)

	err := svc.SaveApp(t.Context(), collectorName, NewApp{
		Name:   "taken",
		Source: &types.SourceSpec{Type: "journalctl", Params: map[string]string{"unit": "taken.service"}},
		Parser: types.ParserSpec{Type: "json", Params: map[string]string{"level_field": "level", "message_field": "msg", "timestamp_field": "time", "timestamp_format": time.RFC3339}},
	})
	if err != nil {
		t.Fatalf("SaveApp: %v", err)
	}

	err = svc.SaveApp(t.Context(), collectorName, NewApp{
		Name:   "taken",
		Source: &types.SourceSpec{Type: "journalctl", Params: map[string]string{"unit": "other.service"}},
		Parser: types.ParserSpec{Type: "json", Params: map[string]string{"level_field": "level", "message_field": "msg", "timestamp_field": "time", "timestamp_format": time.RFC3339}},
	})
	if !errors.Is(err, ErrAppNameTaken) {
		t.Fatalf("SaveApp error = %v, want ErrAppNameTaken", err)
	}
}

// TestSaveAppRejectsABadUptimeCheck pins the submit-time validation: the check
// the form carries has to be one the collector can run.
func TestSaveAppRejectsABadUptimeCheck(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)

	err := svc.SaveApp(t.Context(), collectorName, NewApp{
		Name: "bad-check",
		URL:  "ftp://example.com",
	})
	if !errors.Is(err, ErrAppInvalidCheck) {
		t.Fatalf("SaveApp error = %v, want ErrAppInvalidCheck", err)
	}
}

// TestSaveAppValidatesTheCheckBeforeTheSource covers a form that is wrong twice
// over: the bad URL is reported even though the log source cannot be resolved
// either, so fixing the URL is the next thing the user sees.
func TestSaveAppValidatesTheCheckBeforeTheSource(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	g := &recordingFormatGuesser{
		byService: map[string]types.FormatResultEvent{
			"deploy/barse": {NotFound: true, Error: "source not found"},
		},
	}
	svc.guesser = g

	err := svc.SaveApp(t.Context(), collectorName, NewApp{
		Name:    "barse-web",
		Service: "deploy/barse",
		URL:     "ftp://barse.example.com",
	})
	if !errors.Is(err, ErrAppInvalidCheck) {
		t.Fatalf("SaveApp error = %v, want ErrAppInvalidCheck", err)
	}
	if len(g.services) != 0 {
		t.Fatalf("guessed %q although the check was already invalid", strings.Join(g.services, ","))
	}
}

// TestSaveAppFailedGuessIsNotAnEmptyApp pins that a log source the collector
// cannot resolve is reported as such: the error must not read as an app with
// nothing to collect, which would blame the form for the collector's answer.
func TestSaveAppFailedGuessIsNotAnEmptyApp(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	g := &recordingFormatGuesser{
		byService: map[string]types.FormatResultEvent{
			"deploy/barse": {NotFound: true, Error: `pods "barse" not found`},
		},
	}
	svc.guesser = g

	err := svc.SaveApp(t.Context(), collectorName, NewApp{
		Name:    "barse-web",
		Service: "deploy/barse",
		URL:     "https://barse.example.com/health",
	})
	if !errors.Is(err, ErrAppSourceNotFound) {
		t.Fatalf("SaveApp error = %v, want ErrAppSourceNotFound", err)
	}
	if errors.Is(err, ErrAppNoSource) {
		t.Fatalf("SaveApp error = %v, must not wrap ErrAppNoSource: the form supplied a check", err)
	}
	if !strings.Contains(err.Error(), `pods "barse" not found`) {
		t.Fatalf("SaveApp error = %v, want the collector reason", err)
	}
}

func TestCheckUptimeDefaults(t *testing.T) {
	check, err := CheckUptime(" https://example.com/health ", "", "")
	if err != nil {
		t.Fatalf("CheckUptime: %v", err)
	}
	if check == nil {
		t.Fatal("expected a health check")
	}
	if check.Type != "http" {
		t.Fatalf("type = %q, want http", check.Type)
	}
	if check.Params["url"] != "https://example.com/health" {
		t.Fatalf("url = %q, want the trimmed input", check.Params["url"])
	}
	if check.Params["interval"] != "30s" {
		t.Fatalf("interval = %q, want the 30s default", check.Params["interval"])
	}
	if check.Params["expected_status"] != "200" {
		t.Fatalf("expected status = %q, want the 200 default", check.Params["expected_status"])
	}
}

func TestCheckUptimeEmptyURL(t *testing.T) {
	check, err := CheckUptime("  ", "30s", "200")
	if err != nil {
		t.Fatalf("CheckUptime: %v", err)
	}
	if check != nil {
		t.Fatalf("expected no check for an empty url, got %+v", check)
	}
}

func TestCheckUptimeRejectsBadInput(t *testing.T) {
	if _, err := CheckUptime("ftp://example.com", "30s", "200"); err == nil {
		t.Fatal("expected a non-http url to be rejected")
	}
	if _, err := CheckUptime("https://example.com", "every minute", "200"); err == nil {
		t.Fatal("expected an unparsable interval to be rejected")
	}
	if _, err := CheckUptime("http://:8080/health", "30s", "200"); err == nil {
		t.Fatal("expected a url naming no host to be rejected")
	}
}

func savedApp(t *testing.T, svc *Service, collectorName, appName string) types.AppSpec {
	t.Helper()
	raw, err := svc.FetchCollectorConfig(t.Context(), collectorName)
	if err != nil {
		t.Fatalf("FetchCollectorConfig: %v", err)
	}
	var cfg types.CollectorConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal config %q: %v", raw, err)
	}
	app, ok := cfg.GetApp(appName)
	if !ok {
		t.Fatalf("app %q not in config %s", appName, raw)
	}
	return app
}
