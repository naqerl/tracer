// Package selfapp registers the collector's own systemd service as an app
// during install, so a freshly installed collector tails and reports its own
// logs to the panel without manual configuration.
package selfapp

import (
	"fmt"
	"log/slog"
	"maps"
	"time"

	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/health"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/build"
	"tracer/libs/installx"
)

// defaultHealthURL probes the panel's login page, which the default panel
// install serves on localhost:8080. The probe pauses while the panel is not
// yet running locally and resumes once it is, so the collector's own card
// shows up once the panel serves.
const defaultHealthURL = "http://localhost:8080/auth/login"

// Registered reports whether the collector's own service is already present in
// the config as an app.
func Registered(cfg config.Config) bool {
	for _, app := range cfg.Apps {
		if app.Name == build.CollectorName {
			return true
		}
	}
	return false
}

// Ensure adds the collector's own service as an app when it is not present.
// It is best-effort: on hosts without systemd or journald it skips with a
// warning so install still succeeds.
//
// The parser spec is not guessed from live journal lines — at first install
// the unit has never run and the journal is empty — it is seeded for the slog
// text format every tracer daemon writes to stdout/stderr.
func Ensure(path string, cfg config.Config) (config.Config, error) {
	if Registered(cfg) {
		return cfg, nil
	}
	if !installx.SystemdRunning() {
		slog.Warn("Self-registration skipped, systemd is not running",
			"unit", build.CollectorName)
		return cfg, nil
	}
	if !journaldPresent() {
		slog.Warn("Self-registration skipped, journald not available",
			"unit", build.CollectorName)
		return cfg, nil
	}

	if cfg.Parsers == nil {
		cfg.Parsers = map[string]parser.Spec{}
	}
	parserID := ensureParser(cfg.Parsers, collectorParserSpec())
	cfg.Apps = append(cfg.Apps, config.AppSpec{
		Name:     build.CollectorName,
		ParserID: parserID,
		Source:   collectorSourceSpec(),
		Health:   defaultHealth(),
	})
	if err := cfg.Persist(path); err != nil {
		return cfg, fmt.Errorf("could not persist self-registered app: %w", err)
	}

	slog.Info("Self-registered the collector's own service",
		"app", build.CollectorName, "parser", parserID)
	return cfg, nil
}

// collectorSourceSpec tails the collector's own systemd journal.
func collectorSourceSpec() source.Spec {
	return source.Spec{
		Type: "journalctl",
		Params: map[string]string{
			"unit": build.CollectorName,
		},
	}
}

// collectorParserSpec parses the slog TextHandler output every tracer daemon
// emits; journald stores those lines verbatim.
func collectorParserSpec() parser.Spec {
	return parser.Spec{
		Type: "slogtext",
		Params: map[string]string{
			"level_field":      "level",
			"message_field":    "msg",
			"timestamp_field":  "time",
			"timestamp_format": time.RFC3339Nano,
		},
	}
}

// ensureParser returns the config key of an already registered identical spec,
// or registers spec under the next free "<type>-<unit>" key and returns it.
func ensureParser(parsers map[string]parser.Spec, spec parser.Spec) string {
	for id, s := range parsers {
		if s.Type == spec.Type && maps.Equal(s.Params, spec.Params) {
			return id
		}
	}
	base := spec.Type + "-" + build.CollectorName
	id := base
	for i := 1; ; i++ {
		if _, taken := parsers[id]; !taken {
			parsers[id] = spec
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

// defaultHealth returns an HTTP probe of the panel's login page.
func defaultHealth() *health.Spec {
	cfg := health.HTTPConfig{}.Default()
	cfg.URL = defaultHealthURL
	spec := cfg.Spec()
	return &spec
}

func journaldPresent() bool {
	return installx.FileExists("/run/systemd/journal/socket")
}
