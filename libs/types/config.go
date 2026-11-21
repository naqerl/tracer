package types

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// CollectorConfig is the panel-driven configuration pushed to collectors over
// WebSocket. Both the panel and collector decode this type.
type CollectorConfig struct {
	Apps  []AppSpec    `json:"apps"`
	Stats *StatsConfig `json:"stats,omitempty"`
}

// UnmarshalJSON decodes the config, resolving the legacy format (a shared
// parsers map referenced by parser_id) into each app's inline parser.
func (c *CollectorConfig) UnmarshalJSON(data []byte) error {
	type rawApp struct {
		Name      string           `json:"name"`
		ParserID  string           `json:"parser_id,omitempty"`
		Parser    *ParserSpec      `json:"parser,omitempty"`
		Source    *SourceSpec      `json:"source,omitempty"`
		Lifecycle *LifecycleSpec   `json:"lifecycle,omitempty"`
		Health    *HealthCheckSpec `json:"health,omitempty"`
	}
	type rawConfig struct {
		Parsers map[string]ParserSpec `json:"parsers,omitempty"`
		Apps    []rawApp              `json:"apps"`
		Stats   *StatsConfig          `json:"stats,omitempty"`
	}
	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	config := CollectorConfig{Stats: raw.Stats}
	for _, app := range raw.Apps {
		spec := AppSpec{
			Name:      app.Name,
			Parser:    app.Parser,
			Source:    app.Source,
			Lifecycle: app.Lifecycle,
			Health:    app.Health,
		}
		if spec.Parser == nil && app.ParserID != "" {
			if parser, ok := raw.Parsers[app.ParserID]; ok {
				parser := parser
				spec.Parser = &parser
			}
		}
		config.Apps = append(config.Apps, spec)
	}
	*c = config
	return nil
}

// StatsConfig configures the collector's system resource (CPU, memory, disk)
// sampling. Interval is a Go duration string (e.g. "30s"). It is pushed to
// collectors as part of the panel-driven config.
//
// Threshold fields override the panel-wide globals when non-zero (or non-nil
// for durations). Zero values mean "use the global default".
type StatsConfig struct {
	Interval string `json:"interval"`
	CPU      bool   `json:"cpu"`
	Mem      bool   `json:"mem"`
	Disk     bool   `json:"disk"`

	// Period deltas: minimum change to open a new period.
	CPUDeltaPercent *float64 `json:"cpu_delta_percent,omitempty"`
	MemDeltaKB      *int     `json:"mem_delta_kb,omitempty"`
	DiskDeltaKB     *int     `json:"disk_delta_kb,omitempty"`

	// Alert thresholds: usage percentage above which a notification fires.
	// 0 or nil means disabled.
	CPUThresholdPercent  *float64 `json:"cpu_threshold_percent,omitempty"`
	MemThresholdPercent  *float64 `json:"mem_threshold_percent,omitempty"`
	DiskThresholdPercent *float64 `json:"disk_threshold_percent,omitempty"`

	// Cooldown between resource notifications per collector+resource.
	ResourceNotifyMinutes *int `json:"resource_notify_minutes,omitempty"`
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

// ParserSpec describes a log parser owned by a single app.
type ParserSpec struct {
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

// AppSpec describes a single application the collector should run.
// An app with only Health set (and no Source/Parser) is an uptime-only
// monitor — the collector polls the health endpoint but collects no logs.
type AppSpec struct {
	Name      string           `json:"name"`
	Parser    *ParserSpec      `json:"parser,omitempty"`
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

// Health check types and params shared by the panel and collector.
const (
	HealthTypeHTTP    = "http"
	HealthTypeCommand = "command"

	HealthParamURL            = "url"
	HealthParamMethod         = "method"
	HealthParamExpectedStatus = "expected_status"
	HealthParamCommand        = "command"
	HealthParamExpect         = "expect"
	HealthParamExpectedExit   = "expected_exit"
	HealthParamInterval       = "interval"
	HealthParamTimeout        = "timeout"
	HealthParamAlertDelay     = "alert_delay"
)

// UptimeAlertDelay returns how long an app must stay down before its uptime
// alert is sent. An absent or empty param means no delay: alert immediately.
func (a AppSpec) UptimeAlertDelay() (time.Duration, error) {
	if a.Health == nil {
		return 0, nil
	}
	raw := strings.TrimSpace(a.Health.Params[HealthParamAlertDelay])
	if raw == "" {
		return 0, nil
	}
	delay, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", HealthParamAlertDelay, err)
	}
	if delay < 0 {
		return 0, fmt.Errorf("invalid %s: must not be negative", HealthParamAlertDelay)
	}
	return delay, nil
}

// SourceSpec returns the source spec if set. The second return value reports
// whether a source was configured.
func (a AppSpec) SourceSpec() (*SourceSpec, bool) {
	return a.Source, a.Source != nil
}

// ParserSpec returns the parser spec if set.
func (a AppSpec) ParserSpec() (*ParserSpec, bool) {
	return a.Parser, a.Parser != nil
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
	return a.Source == nil && a.Parser == nil
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
// apps with neither source nor health check, sources without parsers, and
// incomplete parsers.
func (c *CollectorConfig) Validate() error {
	seenApps := make(map[string]bool, len(c.Apps))
	for _, app := range c.Apps {
		if seenApps[app.Name] {
			return fmt.Errorf("duplicate app name: %s", app.Name)
		}
		seenApps[app.Name] = true

		if app.Source == nil && app.Health == nil {
			return fmt.Errorf("app %s has no source and no health check: nothing to do", app.Name)
		}
		if app.Source != nil && app.Parser == nil {
			return fmt.Errorf("app %s has no parser", app.Name)
		}
		if app.Parser != nil {
			required, ok := parserRequiredParams[app.Parser.Type]
			if !ok {
				return fmt.Errorf("app %s: unknown parser type %q", app.Name, app.Parser.Type)
			}
			for _, k := range required {
				if strings.TrimSpace(app.Parser.Params[k]) == "" {
					return fmt.Errorf("app %s: missing required parser param %s", app.Name, k)
				}
			}
		}
		if app.Health != nil {
			if err := validateHealthCheck(app.Health); err != nil {
				return fmt.Errorf("app %s: %w", app.Name, err)
			}
		}
		if _, err := app.UptimeAlertDelay(); err != nil {
			return fmt.Errorf("app %s: %w", app.Name, err)
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
func (c *CollectorConfig) AddApp(appName string, src SourceSpec, parser ParserSpec) error {
	if c.Apps == nil {
		c.Apps = []AppSpec{}
	}
	for _, app := range c.Apps {
		if app.Name == appName {
			return fmt.Errorf("app name %q already exists", appName)
		}
	}
	c.Apps = append(c.Apps, AppSpec{
		Name:   appName,
		Parser: &parser,
		Source: &src,
	})
	return nil
}

// AddUptimeApp appends an uptime-only app (health check, no logs) to the config.
func (c *CollectorConfig) AddUptimeApp(appName string, health *HealthCheckSpec) error {
	if c.Apps == nil {
		c.Apps = []AppSpec{}
	}

	c.Apps = append(c.Apps, AppSpec{
		Name:   appName,
		Health: health,
	})
	return nil
}

// RemoveApp removes an app by name.
func (c *CollectorConfig) RemoveApp(appName string) {
	var kept []AppSpec
	for _, app := range c.Apps {
		if app.Name == appName {
			continue
		}
		kept = append(kept, app)
	}
	c.Apps = kept
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

// SetAppSource replaces the log source and parser of an existing app, turning
// an uptime-only monitor into a log-collecting app.
func (c *CollectorConfig) SetAppSource(name string, src SourceSpec, parser ParserSpec) error {
	for i := range c.Apps {
		if c.Apps[i].Name != name {
			continue
		}
		c.Apps[i].Parser = &parser
		c.Apps[i].Source = &src
		return nil
	}
	return fmt.Errorf("app %s not found", name)
}

// SetAppHealth sets or replaces the health check of an existing app, leaving
// any log source in place.
func (c *CollectorConfig) SetAppHealth(name string, health *HealthCheckSpec) error {
	for i := range c.Apps {
		if c.Apps[i].Name != name {
			continue
		}
		c.Apps[i].Health = health
		return nil
	}
	return fmt.Errorf("app %s not found", name)
}

// ClearAppHealth removes the health check from an existing app.
func (c *CollectorConfig) ClearAppHealth(name string) error {
	for i := range c.Apps {
		if c.Apps[i].Name != name {
			continue
		}
		c.Apps[i].Health = nil
		return nil
	}
	return fmt.Errorf("app %s not found", name)
}

// RenameApp renames an app from oldName to newName.
func (c *CollectorConfig) RenameApp(oldName, newName string) error {
	for i := range c.Apps {
		if c.Apps[i].Name != oldName {
			continue
		}
		for _, other := range c.Apps {
			if other.Name == newName {
				return fmt.Errorf("app name %q already exists", newName)
			}
		}
		c.Apps[i].Name = newName
		return nil
	}
	return fmt.Errorf("app %s not found", oldName)
}

// ClearAppSource removes the log source and parser from an existing app.
func (c *CollectorConfig) ClearAppSource(name string) error {
	for i := range c.Apps {
		if c.Apps[i].Name != name {
			continue
		}
		c.Apps[i].Parser = nil
		c.Apps[i].Source = nil
		return nil
	}
	return fmt.Errorf("app %s not found", name)
}

func validateHealthCheck(spec *HealthCheckSpec) error {
	switch strings.ToLower(strings.TrimSpace(spec.Type)) {
	case "":
		return fmt.Errorf("health type is required")
	case "http":
		return nil
	case "command":
		if strings.TrimSpace(spec.Params[HealthParamCommand]) == "" {
			return fmt.Errorf("command health check requires a command")
		}
		expect := strings.TrimSpace(spec.Params[HealthParamExpect])
		if expect != "" {
			if _, err := regexp.Compile("(?m)" + expect); err != nil {
				return fmt.Errorf("could not compile expect regexp: %w", err)
			}
		}
		return nil
	case "systemd", "podman":
		return fmt.Errorf("health type %q is not implemented yet", spec.Type)
	default:
		return fmt.Errorf("unknown health type %q", spec.Type)
	}
}
