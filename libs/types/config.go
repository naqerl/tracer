package types

import (
	"fmt"
	"strings"
	"time"
)

// CollectorConfig is the panel-driven configuration pushed to collectors over
// WebSocket. Both the panel and collector decode this type.
type CollectorConfig struct {
	Parsers map[string]ParserSpec `json:"parsers"`
	Apps    []AppSpec             `json:"apps"`
	Stats   *StatsConfig          `json:"stats,omitempty"`
}

// StatsConfig configures the collector's system resource (CPU, memory, disk)
// sampling. Interval is a Go duration string (e.g. "30s"). It is pushed to
// collectors as part of the panel-driven config.
type StatsConfig struct {
	Interval string `json:"interval"`
	CPU      bool   `json:"cpu"`
	Mem      bool   `json:"mem"`
	Disk     bool   `json:"disk"`
}

// DefaultStatsConfig returns the sampling settings used when a collector has
// not been configured by the panel: every metric on a 30s interval.
func DefaultStatsConfig() StatsConfig {
	return StatsConfig{
		Interval: "30s",
		CPU:      true,
		Mem:      true,
		Disk:     true,
	}
}

// GetStats returns the stats config, falling back to defaults when unset.
func (c *CollectorConfig) GetStats() StatsConfig {
	if c.Stats == nil {
		return DefaultStatsConfig()
	}
	return *c.Stats
}

// ParserSpec describes a log parser.
type ParserSpec struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

// AppSpec describes a single application the collector should run.
// An app with only Health set (and empty Source/ParserID) is an uptime-only
// monitor — the collector polls the health endpoint but collects no logs.
type AppSpec struct {
	Name      string           `json:"name"`
	ParserID  string           `json:"parser_id,omitempty"`
	Source    *SourceSpec      `json:"source,omitempty"`
	Lifecycle *LifecycleSpec   `json:"lifecycle,omitempty"`
	Health    *HealthCheckSpec `json:"health,omitempty"`
}

// SourceSpec describes a log source.
type SourceSpec struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

// LifecycleSpec describes lifecycle detection patterns.
type LifecycleSpec struct {
	StartPattern   string `json:"start_pattern"`
	RestartPattern string `json:"restart_pattern"`
	ExitPattern    string `json:"exit_pattern"`
}

// HealthCheckSpec describes a health check configuration.
type HealthCheckSpec struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

// Source returns the source spec if set. The second return value reports
// whether a source was configured.
func (a AppSpec) SourceSpec() (*SourceSpec, bool) {
	return a.Source, a.Source != nil
}

// ParserIDValue returns the parser ID and whether one was configured.
func (a AppSpec) ParserIDValue() (string, bool) {
	return a.ParserID, a.ParserID != ""
}

// LifecycleDetectorSpec returns the lifecycle spec if set.
func (a AppSpec) LifecycleDetectorSpec() (*LifecycleSpec, bool) {
	return a.Lifecycle, a.Lifecycle != nil
}

// HealthSpec returns the health check spec if set.
func (a AppSpec) HealthSpec() (*HealthCheckSpec, bool) {
	return a.Health, a.Health != nil
}

// IsHealthOnly reports whether this app only performs health checks with no
// log collection.
func (a AppSpec) IsHealthOnly() bool {
	return a.Source == nil && a.ParserID == ""
}

// EnsureSelfApp adds the collector's own systemd journal as an app if not
// already present. The self-app lets the panel show the collector's own
// health and logs without manual configuration.
func (c *CollectorConfig) EnsureSelfApp(collectorName string) {
	for _, app := range c.Apps {
		if app.Name == collectorName {
			return
		}
	}

	if c.Parsers == nil {
		c.Parsers = map[string]ParserSpec{}
	}
	parserID := "slogtext-" + collectorName
	if _, taken := c.Parsers[parserID]; !taken {
		c.Parsers[parserID] = ParserSpec{
			Type: "slogtext",
			Params: map[string]string{
				"level_field":      "level",
				"message_field":    "msg",
				"timestamp_field":  "time",
				"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
			},
		}
	}

	c.Apps = append(c.Apps, AppSpec{
		Name:     collectorName,
		ParserID: parserID,
		Source: &SourceSpec{
			Type: "journalctl",
			Params: map[string]string{
				"unit": collectorName,
			},
		},
	})
}

// parserRequiredParams lists the params every parser of a given type must
// define. Configs referencing an unknown type or missing a required param are
// rejected before they reach the collector, whose parser constructor panics on
// incomplete specs.
var parserRequiredParams = map[string][]string{
	"json":     {"level_field", "message_field", "timestamp_field", "timestamp_format"},
	"slogtext": {"level_field", "message_field", "timestamp_field", "timestamp_format"},
	"regex":    {"pattern", "level_field", "message_field", "timestamp_field", "timestamp_format"},
}

// Validate checks the config for structural errors: duplicate app names,
// duplicate parser IDs, references to missing parsers, incomplete parsers,
// and apps with neither source nor health check.
func (c *CollectorConfig) Validate() error {
	if c.Parsers == nil {
		c.Parsers = map[string]ParserSpec{}
	}

	for id, p := range c.Parsers {
		required, ok := parserRequiredParams[p.Type]
		if !ok {
			return fmt.Errorf("parser id=%s: unknown parser type %q", id, p.Type)
		}
		for _, k := range required {
			if strings.TrimSpace(p.Params[k]) == "" {
				return fmt.Errorf("parser id=%s: missing required param %s", id, k)
			}
		}
	}

	seenApps := make(map[string]bool, len(c.Apps))
	for _, app := range c.Apps {
		if seenApps[app.Name] {
			return fmt.Errorf("duplicate app name: %s", app.Name)
		}
		seenApps[app.Name] = true

		if app.IsHealthOnly() && app.Health == nil {
			return fmt.Errorf("app %s has no source and no health check: nothing to do", app.Name)
		}
		if !app.IsHealthOnly() {
			if _, ok := c.Parsers[app.ParserID]; !ok {
				return fmt.Errorf("parser with id=%s not found", app.ParserID)
			}
		}
	}

	if c.Stats != nil {
		if _, err := time.ParseDuration(c.Stats.Interval); err != nil {
			return fmt.Errorf("invalid stats interval %q: %w", c.Stats.Interval, err)
		}
	}

	return nil
}

// AddApp appends an app with a log source and parser to the config.
// It derives the parser ID from the parser type and app name.
func (c *CollectorConfig) AddApp(appName string, src SourceSpec, parser ParserSpec) error {
	if c.Parsers == nil {
		c.Parsers = map[string]ParserSpec{}
	}
	if c.Apps == nil {
		c.Apps = []AppSpec{}
	}

	parserID := parser.Type + "-" + strings.ReplaceAll(appName, " ", "-")
	if _, exists := c.Parsers[parserID]; exists {
		return fmt.Errorf("parser id %q already exists; app names must be unique per parser type", parserID)
	}
	c.Parsers[parserID] = parser

	c.Apps = append(c.Apps, AppSpec{
		Name:     appName,
		ParserID: parserID,
		Source:   &src,
	})
	return nil
}

// AddUptimeApp appends an uptime-only app (health check, no logs) to the config.
func (c *CollectorConfig) AddUptimeApp(appName, url, interval, expectedStatus string) error {
	if c.Apps == nil {
		c.Apps = []AppSpec{}
	}

	c.Apps = append(c.Apps, AppSpec{
		Name: appName,
		Health: &HealthCheckSpec{
			Type: "http",
			Params: map[string]string{
				"url":             url,
				"interval":        interval,
				"expected_status": expectedStatus,
			},
		},
	})
	return nil
}

// RemoveApp removes an app by name and drops any parsers it no longer
// references.
func (c *CollectorConfig) RemoveApp(appName string) {
	var kept []AppSpec
	for _, app := range c.Apps {
		if app.Name == appName {
			continue
		}
		kept = append(kept, app)
	}
	c.Apps = kept

	used := make(map[string]bool, len(c.Apps))
	for _, app := range c.Apps {
		if app.ParserID != "" {
			used[app.ParserID] = true
		}
	}
	for pid := range c.Parsers {
		if !used[pid] {
			delete(c.Parsers, pid)
		}
	}
}

// GetApp returns the spec of an app by name and whether it exists.
func (c *CollectorConfig) GetApp(name string) (AppSpec, bool) {
	for _, app := range c.Apps {
		if app.Name == name {
			return app, true
		}
	}
	return AppSpec{}, false
}

// SetAppSource replaces the log source and parser of an existing app, changing
// it from an uptime-only monitor into a log-collecting app.
func (c *CollectorConfig) SetAppSource(name string, src SourceSpec, parser ParserSpec) error {
	for i := range c.Apps {
		if c.Apps[i].Name != name {
			continue
		}
		if c.Parsers == nil {
			c.Parsers = map[string]ParserSpec{}
		}
		oldParserID := c.Apps[i].ParserID
		newParserID := parser.Type + "-" + strings.ReplaceAll(name, " ", "-")
		c.Parsers[newParserID] = parser
		c.Apps[i].ParserID = newParserID
		c.Apps[i].Source = &src
		if oldParserID != "" && oldParserID != newParserID {
			delete(c.Parsers, oldParserID)
		}
		return nil
	}
	return fmt.Errorf("app %s not found", name)
}

// SetAppHealth replaces the health check of an existing app, changing it into
// an uptime-only monitor that collects no logs.
func (c *CollectorConfig) SetAppHealth(name, url, interval, expectedStatus string) error {
	for i := range c.Apps {
		if c.Apps[i].Name != name {
			continue
		}
		if c.Apps[i].ParserID != "" {
			if c.Parsers != nil {
				delete(c.Parsers, c.Apps[i].ParserID)
			}
			c.Apps[i].ParserID = ""
			c.Apps[i].Source = nil
		}
		c.Apps[i].Health = &HealthCheckSpec{
			Type: "http",
			Params: map[string]string{
				"url":             url,
				"interval":        interval,
				"expected_status": expectedStatus,
			},
		}
		return nil
	}
	return fmt.Errorf("app %s not found", name)
}
