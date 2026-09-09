package types

import (
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
		Parsers: map[string]ParserSpec{"p": {Type: "yaml"}},
		Apps:    []AppSpec{{Name: "a", ParserID: "p", Source: &SourceSpec{Type: "journalctl"}}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for unknown parser type")
	}
}

func TestValidateRejectsMissingParserParam(t *testing.T) {
	cfg := CollectorConfig{
		Parsers: map[string]ParserSpec{"p": {Type: "json", Params: map[string]string{"level_field": "level"}}},
		Apps:    []AppSpec{{Name: "a", ParserID: "p", Source: &SourceSpec{Type: "journalctl"}}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing parser param")
	}
}

func TestValidateAcceptsCompleteParser(t *testing.T) {
	cfg := CollectorConfig{
		Parsers: map[string]ParserSpec{"p": {Type: "json", Params: map[string]string{
			"level_field":      "level",
			"message_field":    "msg",
			"timestamp_field":  "time",
			"timestamp_format": "2006-01-02T15:04:05",
		}}},
		Apps: []AppSpec{{Name: "a", ParserID: "p", Source: &SourceSpec{Type: "journalctl"}}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with complete parser: %v", err)
	}
}

func TestRemoveAppKeepsSharedParser(t *testing.T) {
	parser := ParserSpec{Type: "json", Params: map[string]string{
		"level_field":      "level",
		"message_field":    "msg",
		"timestamp_field":  "time",
		"timestamp_format": "2006-01-02T15:04:05",
	}}
	cfg := CollectorConfig{
		Parsers: map[string]ParserSpec{"shared": parser},
		Apps: []AppSpec{
			{Name: "nginx", ParserID: "shared", Source: &SourceSpec{Type: "journalctl"}},
			{Name: "redis", ParserID: "shared", Source: &SourceSpec{Type: "journalctl"}},
		},
	}

	cfg.RemoveApp("redis")
	if len(cfg.Apps) != 1 || cfg.Apps[0].Name != "nginx" {
		t.Fatalf("apps = %v, want only nginx", cfg.Apps)
	}
	if _, ok := cfg.Parsers["shared"]; !ok {
		t.Fatalf("shared parser must survive removal of one referencing app, parsers = %v", cfg.Parsers)
	}

	cfg.RemoveApp("nginx")
	if len(cfg.Parsers) != 0 {
		t.Fatalf("parsers = %v, want none after removing last referencing app", cfg.Parsers)
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

	if _, ok := cfg.GetApp("missing"); ok {
		t.Fatal("expected missing app to not exist")
	}
}

func TestSetAppSource(t *testing.T) {
	var cfg CollectorConfig
	if err := cfg.AddUptimeApp("nginx", "https://example.com", "30s", "200"); err != nil {
		t.Fatalf("AddUptimeApp: %v", err)
	}

	src := SourceSpec{Type: "file", Params: map[string]string{"path": "/var/log/nginx.log"}}
	parser := ParserSpec{Type: "slogtext", Params: map[string]string{"level_field": "level"}}
	if err := cfg.SetAppSource("nginx", src, parser); err != nil {
		t.Fatalf("SetAppSource: %v", err)
	}

	app, _ := cfg.GetApp("nginx")
	if app.IsHealthOnly() {
		t.Fatal("expected app to now have a source")
	}
	if app.Source.Type != "file" {
		t.Fatalf("app source type = %q, want file", app.Source.Type)
	}
	stored := cfg.Parsers[app.ParserID]
	if stored.Type != "slogtext" {
		t.Fatalf("stored parser type = %q, want slogtext", stored.Type)
	}
	if stored.Params["level_field"] != "level" {
		t.Fatalf("stored parser params = %v", stored.Params)
	}

	if err := cfg.SetAppSource("missing", src, parser); err == nil {
		t.Fatal("expected error for missing app")
	}
}

func TestSetAppHealth(t *testing.T) {
	var cfg CollectorConfig
	if err := cfg.AddApp("nginx", SourceSpec{Type: "journalctl"}, ParserSpec{Type: "json"}); err != nil {
		t.Fatalf("AddApp: %v", err)
	}

	if err := cfg.SetAppHealth("nginx", "https://example.com", "60s", "204"); err != nil {
		t.Fatalf("SetAppHealth: %v", err)
	}

	app, _ := cfg.GetApp("nginx")
	if !app.IsHealthOnly() {
		t.Fatal("expected app to become health-only after SetAppHealth")
	}
	if app.Source != nil {
		t.Fatal("expected source to be cleared")
	}
	if app.Health.Params["url"] != "https://example.com" || app.Health.Params["interval"] != "60s" || app.Health.Params["expected_status"] != "204" {
		t.Fatalf("health params = %v", app.Health.Params)
	}

	if err := cfg.SetAppHealth("missing", "https://example.com", "60s", "200"); err == nil {
		t.Fatal("expected error for missing app")
	}
}
