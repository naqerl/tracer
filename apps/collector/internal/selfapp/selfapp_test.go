package selfapp

import (
	"testing"
	"time"

	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/parser"
	"tracer/build"
)

func TestRegistered(t *testing.T) {
	if Registered(config.Default()) {
		t.Fatal("fresh config must not be registered")
	}

	cfg := config.Default()
	cfg.Apps = append(cfg.Apps, config.AppSpec{
		Name:     build.CollectorName,
		ParserID: "slogtext-tracer-collector",
		Source:   collectorSourceSpec(),
		Health:   defaultHealth(),
	})
	if !Registered(cfg) {
		t.Fatal("config with the self app must be registered")
	}
	if len(cfg.Apps) != 1 {
		t.Fatalf("want exactly one self app, got %d", len(cfg.Apps))
	}
}

func TestCollectorParserParsesDaemonLogLine(t *testing.T) {
	p := parser.New(collectorParserSpec())

	entry, err := p.Parse(`time=2026-08-23T12:16:54.123+00:00 level=WARN msg="self check failed" unit=tracer-collector`)
	if err != nil {
		t.Fatalf("could not parse daemon log line: %v", err)
	}
	if want := "self check failed"; entry.Message != want {
		t.Errorf("message = %q, want %q", entry.Message, want)
	}
	if got, want := string(entry.Level), "warn"; got != want {
		t.Errorf("level = %q, want %q", got, want)
	}
	if want := time.Date(2026, 8, 23, 12, 16, 54, 123e6, time.UTC); !entry.Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v", entry.Timestamp, want)
	}
	if got := entry.Extra["unit"]; got != build.CollectorName {
		t.Errorf("extra unit = %q, want %q", got, build.CollectorName)
	}
}

func TestEnsureParser(t *testing.T) {
	spec := collectorParserSpec()

	parsers := map[string]parser.Spec{}
	id := ensureParser(parsers, spec)
	if id != "slogtext-tracer-collector" {
		t.Fatalf("id = %q, want slogtext-tracer-collector", id)
	}
	if again := ensureParser(parsers, spec); again != id {
		t.Fatalf("identical spec must reuse id %q, got %q", id, again)
	}

	conflict := map[string]parser.Spec{
		"slogtext-tracer-collector": {Type: "slogtext", Params: map[string]string{"level_field": "other"}},
	}
	if id := ensureParser(conflict, spec); id != "slogtext-tracer-collector-1" {
		t.Fatalf("taken id must allocate next free name, got %q", id)
	}
}
