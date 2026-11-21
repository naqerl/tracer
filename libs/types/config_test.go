package types

import (
	"encoding/json"
	"testing"
)

func TestDefaultStatsConfig(t *testing.T) {
	want := StatsConfig{Interval: "30s", CPU: true, Mem: true, Disk: true}
	if got := DefaultStatsConfig(); got != want {
		t.Fatalf("DefaultStatsConfig() = %+v, want %+v", got, want)
	}
}

func TestGetStatsFallsBackToDefaults(t *testing.T) {
	var cfg CollectorConfig
	if got := cfg.GetStats(); got != DefaultStatsConfig() {
		t.Fatalf("GetStats() = %+v, want defaults", got)
	}

	custom := StatsConfig{Interval: "10s", CPU: true, Mem: false, Disk: false}
	cfg.Stats = &custom
	if got := cfg.GetStats(); got != custom {
		t.Fatalf("GetStats() = %+v, want %+v", got, custom)
	}
}

func TestValidateStatsInterval(t *testing.T) {
	var cfg CollectorConfig
	cfg.Stats = &StatsConfig{Interval: "10s", CPU: true, Mem: true, Disk: true}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with valid interval: %v", err)
	}

	cfg.Stats.Interval = "not-a-duration"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid interval")
	}
}

func TestValidateRejectsUnknownParserType(t *testing.T) {
	cfg := CollectorConfig{
		Apps: []AppSpec{{Name: "a", Parser: &ParserSpec{Type: "yaml"}, Source: &SourceSpec{Type: "journalctl"}}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for unknown parser type")
	}
}

func TestValidateRejectsMissingParserParam(t *testing.T) {
	cfg := CollectorConfig{
		Apps: []AppSpec{{Name: "a", Parser: &ParserSpec{Type: "json", Params: map[string]string{"level_field": "level"}}, Source: &SourceSpec{Type: "journalctl"}}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing parser param")
	}
}

func TestValidateAcceptsCompleteParser(t *testing.T) {
	cfg := CollectorConfig{
		Apps: []AppSpec{{Name: "a", Source: &SourceSpec{Type: "journalctl"}, Parser: &ParserSpec{Type: "json", Params: map[string]string{
			"level_field":      "level",
			"message_field":    "msg",
			"timestamp_field":  "time",
			"timestamp_format": "2006-01-02T15:04:05",
		}}}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with complete parser: %v", err)
	}
}

func TestValidateHealthTypes(t *testing.T) {
	valid := []*HealthCheckSpec{
		testHTTPHealth("https://example.com", "30s", "200"),
		{Type: "command", Params: map[string]string{"command": "true"}},
	}
	for _, health := range valid {
		cfg := CollectorConfig{Apps: []AppSpec{{Name: "a", Health: health}}}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate(%+v) = %v, want nil", health, err)
		}
	}

	invalid := []*HealthCheckSpec{
		{Type: "", Params: map[string]string{"url": "https://example.com"}},
		{Type: "teapot", Params: map[string]string{"command": "true"}},
		{Type: "command", Params: map[string]string{}},
		{Type: "command", Params: map[string]string{"command": "true", "expect": "("}},
		{Type: "systemd", Params: map[string]string{"unit": "nginx.service"}},
		{Type: "podman", Params: map[string]string{"container": "api"}},
	}
	for _, health := range invalid {
		cfg := CollectorConfig{Apps: []AppSpec{{Name: "a", Health: health}}}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("Validate(%+v) = nil, want an error", health)
		}
	}
}

func TestValidateRejectsSourceWithoutParser(t *testing.T) {
	cfg := CollectorConfig{
		Apps: []AppSpec{{Name: "a", Source: &SourceSpec{Type: "journalctl"}}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for source without parser")
	}
}

func TestUnmarshalJSONMigratesLegacyParserRefs(t *testing.T) {
	raw := `{"parsers":{"json":{"type":"json","params":{"level_field":"level","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05"}}},"apps":[{"name":"a","parser_id":"json","source":{"type":"journalctl","params":{}}},{"name":"b","health":{"type":"http","params":{"url":"http://example.com"}}}]}`
	var cfg CollectorConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	app, ok := cfg.GetApp("a")
	if !ok {
		t.Fatal("expected app a to exist")
	}
	if app.Parser == nil || app.Parser.Type != "json" || app.Parser.Params["level_field"] != "level" {
		t.Fatalf("app a parser = %+v, want resolved legacy parser", app.Parser)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("migrated config must be valid: %v", err)
	}

	healthOnly, ok := cfg.GetApp("b")
	if !ok || !healthOnly.IsHealthOnly() {
		t.Fatalf("app b = %+v, want health-only", healthOnly)
	}
}

func TestMarshalJSONWritesInlineParser(t *testing.T) {
	cfg := CollectorConfig{
		Apps: []AppSpec{{Name: "a", Parser: &ParserSpec{Type: "json", Params: map[string]string{"level_field": "level"}}, Source: &SourceSpec{Type: "journalctl"}}},
	}
	got, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var round CollectorConfig
	if err := json.Unmarshal(got, &round); err != nil {
		t.Fatalf("round trip: %v", err)
	}
	app, ok := round.GetApp("a")
	if !ok || app.Parser == nil || app.Parser.Type != "json" {
		t.Fatalf("round-tripped app = %+v, want inline parser", app)
	}
}

func TestRemoveApp(t *testing.T) {
	cfg := CollectorConfig{
		Apps: []AppSpec{
			{Name: "nginx", Parser: &ParserSpec{Type: "json"}, Source: &SourceSpec{Type: "journalctl"}},
			{Name: "redis", Parser: &ParserSpec{Type: "json"}, Source: &SourceSpec{Type: "journalctl"}},
		},
	}

	cfg.RemoveApp("redis")
	if len(cfg.Apps) != 1 || cfg.Apps[0].Name != "nginx" {
		t.Fatalf("apps = %v, want only nginx", cfg.Apps)
	}

	cfg.RemoveApp("nginx")
	if len(cfg.Apps) != 0 {
		t.Fatalf("apps = %v, want none", cfg.Apps)
	}
}

func TestGetApp(t *testing.T) {
	var cfg CollectorConfig
	if err := cfg.AddApp("nginx", SourceSpec{Type: "journalctl"}, ParserSpec{Type: "json"}); err != nil {
		t.Fatalf("AddApp: %v", err)
	}

	app, ok := cfg.GetApp("nginx")
	if !ok {
		t.Fatal("expected app nginx to exist")
	}
	if app.Source.Type != "journalctl" {
		t.Fatalf("app source type = %q, want journalctl", app.Source.Type)
	}
	if app.Parser == nil || app.Parser.Type != "json" {
		t.Fatalf("app parser = %+v, want inline json parser", app.Parser)
	}

	if _, ok := cfg.GetApp("missing"); ok {
		t.Fatal("expected missing app to not exist")
	}
}

func TestAddAppRejectsDuplicateName(t *testing.T) {
	var cfg CollectorConfig
	if err := cfg.AddApp("nginx", SourceSpec{Type: "journalctl"}, ParserSpec{Type: "json"}); err != nil {
		t.Fatalf("AddApp: %v", err)
	}
	if err := cfg.AddApp("nginx", SourceSpec{Type: "journalctl"}, ParserSpec{Type: "slogtext"}); err == nil {
		t.Fatal("expected duplicate app name error")
	}
}

func TestSetAppSource(t *testing.T) {
	var cfg CollectorConfig
	if err := cfg.AddUptimeApp("nginx", testHTTPHealth("https://example.com", "30s", "200")); err != nil {
		t.Fatalf("AddUptimeApp: %v", err)
	}

	src := SourceSpec{Type: "file", Params: map[string]string{"path": "/var/log/nginx.log"}}
	parser := ParserSpec{Type: "slogtext", Params: map[string]string{
		"level_field":      "level",
		"message_field":    "msg",
		"timestamp_field":  "time",
		"timestamp_format": "2006-01-02T15:04:05",
	}}
	if err := cfg.SetAppSource("nginx", src, parser); err != nil {
		t.Fatalf("SetAppSource: %v", err)
	}

	app, _ := cfg.GetApp("nginx")
	if app.IsHealthOnly() {
		t.Fatal("expected app to now have a source")
	}
	if app.Health == nil {
		t.Fatal("expected health check to survive SetAppSource")
	}
	if app.Source.Type != "file" {
		t.Fatalf("app source type = %q, want file", app.Source.Type)
	}
	if app.Parser == nil || app.Parser.Type != "slogtext" || app.Parser.Params["level_field"] != "level" || app.Parser.Params["message_field"] != "msg" {
		t.Fatalf("app parser = %+v, want inline slogtext parser", app.Parser)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config must stay valid after SetAppSource: %v", err)
	}

	if err := cfg.SetAppSource("missing", src, parser); err == nil {
		t.Fatal("expected error for missing app")
	}
}

func TestSetAppHealthKeepsSource(t *testing.T) {
	var cfg CollectorConfig
	if err := cfg.AddApp("nginx", SourceSpec{Type: "journalctl"}, ParserSpec{Type: "json"}); err != nil {
		t.Fatalf("AddApp: %v", err)
	}

	if err := cfg.SetAppHealth("nginx", testHTTPHealth("https://example.com", "60s", "204")); err != nil {
		t.Fatalf("SetAppHealth: %v", err)
	}

	app, _ := cfg.GetApp("nginx")
	if app.IsHealthOnly() {
		t.Fatal("expected log source to survive SetAppHealth")
	}
	if app.Source == nil || app.Source.Type != "journalctl" {
		t.Fatalf("source = %+v, want journalctl", app.Source)
	}
	if app.Parser == nil {
		t.Fatal("expected parser to survive SetAppHealth")
	}
	if app.Health.Params["url"] != "https://example.com" || app.Health.Params["interval"] != "60s" || app.Health.Params["expected_status"] != "204" {
		t.Fatalf("health params = %v", app.Health.Params)
	}

	if err := cfg.SetAppHealth("missing", testHTTPHealth("https://example.com", "60s", "200")); err == nil {
		t.Fatal("expected error for missing app")
	}
}

func TestClearAppHealth(t *testing.T) {
	var cfg CollectorConfig
	if err := cfg.AddApp("nginx", SourceSpec{Type: "journalctl"}, ParserSpec{Type: "json"}); err != nil {
		t.Fatalf("AddApp: %v", err)
	}
	if err := cfg.SetAppHealth("nginx", testHTTPHealth("https://example.com", "30s", "200")); err != nil {
		t.Fatalf("SetAppHealth: %v", err)
	}

	if err := cfg.ClearAppHealth("nginx"); err != nil {
		t.Fatalf("ClearAppHealth: %v", err)
	}
	app, _ := cfg.GetApp("nginx")
	if app.Health != nil {
		t.Fatal("expected health to be cleared")
	}
	if app.Source == nil {
		t.Fatal("expected source to remain")
	}

	if err := cfg.ClearAppHealth("missing"); err == nil {
		t.Fatal("expected error for missing app")
	}
}

func TestClearAppSource(t *testing.T) {
	var cfg CollectorConfig
	if err := cfg.AddApp("nginx", SourceSpec{Type: "journalctl"}, ParserSpec{Type: "json"}); err != nil {
		t.Fatalf("AddApp: %v", err)
	}
	if err := cfg.SetAppHealth("nginx", testHTTPHealth("https://example.com", "30s", "200")); err != nil {
		t.Fatalf("SetAppHealth: %v", err)
	}

	if err := cfg.ClearAppSource("nginx"); err != nil {
		t.Fatalf("ClearAppSource: %v", err)
	}
	app, _ := cfg.GetApp("nginx")
	if !app.IsHealthOnly() {
		t.Fatal("expected app to be health-only after clearing source")
	}
	if app.Source != nil {
		t.Fatal("expected source to be cleared")
	}
	if app.Parser != nil {
		t.Fatal("expected parser to be cleared")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config must stay valid after ClearAppSource: %v", err)
	}

	if err := cfg.ClearAppSource("missing"); err == nil {
		t.Fatal("expected error for missing app")
	}
}

func TestRenameApp(t *testing.T) {
	var cfg CollectorConfig
	if err := cfg.AddApp("nginx", SourceSpec{Type: "journalctl"}, ParserSpec{Type: "json", Params: map[string]string{
		"level_field":      "level",
		"message_field":    "msg",
		"timestamp_field":  "time",
		"timestamp_format": "2006-01-02T15:04:05",
	}}); err != nil {
		t.Fatalf("AddApp: %v", err)
	}

	if err := cfg.RenameApp("nginx", "nginx-new"); err != nil {
		t.Fatalf("RenameApp: %v", err)
	}
	app, ok := cfg.GetApp("nginx-new")
	if !ok {
		t.Fatal("expected renamed app to exist")
	}
	if app.Parser == nil || app.Parser.Type != "json" {
		t.Fatalf("renamed app parser = %+v, want inline parser kept", app.Parser)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config must stay valid after rename: %v", err)
	}

	if err := cfg.RenameApp("nginx-new", "nginx"); err != nil {
		t.Fatalf("RenameApp back: %v", err)
	}
	if err := cfg.RenameApp("nginx-new", "nginx"); err == nil {
		t.Fatal("expected duplicate name error")
	}
	if err := cfg.RenameApp("missing", "x"); err == nil {
		t.Fatal("expected error for missing app")
	}
}

func testHTTPHealth(url, interval, expectedStatus string) *HealthCheckSpec {
	return &HealthCheckSpec{
		Type: "http",
		Params: map[string]string{
			"url":             url,
			"interval":        interval,
			"expected_status": expectedStatus,
		},
	}
}
