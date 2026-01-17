// TODO: Decompose config package for collector and panel, they should not live in one package
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	configPathEnv     = "TRACER_CONFIG"
	defaultConfigPath = "/etc/tracer/config.toml"
)

type Collector struct {
	Name             string  `toml:"name"` // Optional collector name, uses IP if not set
	PanelEndpoint    string  `toml:"panel_endpoint"`
	StatsInterval    string  `toml:"stats_interval"`     // How often to send system stats (e.g., "30s", "1m")
	DataDir          string  `toml:"data_dir"`           // Directory to monitor for disk usage
	CPUAlertPercent  float64 `toml:"cpu_alert_percent"`  // CPU usage alert threshold (percentage)
	MemAlertPercent  float64 `toml:"mem_alert_percent"`  // Memory usage alert threshold (percentage)
	DiskAlertPercent float64 `toml:"disk_alert_percent"` // Disk usage alert threshold (percentage)
	Apps             []App   `toml:"apps"`
}

type App struct {
	Name    string `toml:"name"`
	Path    string `toml:"path"`
	Format  string `toml:"format"` // "json", "regex", or "journalctl"
	Pattern string `toml:"pattern,omitempty"`

	// Field mappings for regex named groups or JSON fields
	TimestampField string `toml:"timestamp_field"`
	LevelField     string `toml:"level_field"`
	TraceIDField   string `toml:"trace_id_field"`
	MessageField   string `toml:"message_field"`

	// Journalctl-specific settings
	Unit string `toml:"unit,omitempty"` // systemd unit name for journalctl format

	// Optional: regex pattern to detect app startup
	// When a log line matches this pattern, a startup notification is sent
	StartupPattern string `toml:"startup_pattern,omitempty"`
}

func LoadCollector(path string) (*Collector, error) {
	if path == "" {
		path = os.Getenv(configPathEnv)
	}
	if path == "" {
		path = defaultConfigPath
	}

	var cfg Collector
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func (c *Collector) validate() error {
	if c.PanelEndpoint == "" {
		return fmt.Errorf("panel_endpoint is required")
	}

	for i, app := range c.Apps {
		if app.Name == "" {
			return fmt.Errorf("apps[%d].name is required", i)
		}
		if app.Format != "journalctl" && app.Path == "" {
			return fmt.Errorf("apps[%d].path is required", i)
		}
		if app.Format != "json" && app.Format != "regex" && app.Format != "journalctl" {
			return fmt.Errorf("apps[%d].format must be 'json', 'regex', or 'journalctl'", i)
		}
		if app.Format == "regex" && app.Pattern == "" {
			return fmt.Errorf("apps[%d].pattern is required for regex format", i)
		}
		if app.Format == "journalctl" {
			if app.Unit == "" {
				return fmt.Errorf("apps[%d].unit is required for journalctl format", i)
			}
		}
	}

	return nil
}

// Panel configuration
type Panel struct {
	Port          string  `toml:"port"`
	DataDir       string  `toml:"data_dir"`
	SampleRate    float64 `toml:"sample_rate"`
	RetentionDays int     `toml:"retention_days"`
	PanelURL      string  `toml:"panel_url"` // Base URL for the panel (e.g., "https://tracer.example.com")
	Auth          Auth    `toml:"auth"`
}

type Auth struct {
	SessionDuration string `toml:"session_duration"`
}

const (
	panelConfigPathEnv     = "TRACER_PANEL_CONFIG"
	defaultPanelConfigPath = "/etc/tracer/panel.toml"
)

func LoadPanel(path string) (*Panel, error) {
	if path == "" {
		path = os.Getenv(panelConfigPathEnv)
	}
	if path == "" {
		path = defaultPanelConfigPath
	}

	var cfg Panel
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}

	// Apply defaults
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/tracer"
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 0.1 // 10% of non-error logs
	}
	if cfg.RetentionDays == 0 {
		cfg.RetentionDays = 90 // 3 months default
	}

	// Default session duration
	if cfg.Auth.SessionDuration == "" {
		cfg.Auth.SessionDuration = "24h"
	}

	return &cfg, nil
}

// SessionDurationParsed returns the parsed session duration
func (p *Panel) SessionDurationParsed() time.Duration {
	d, err := time.ParseDuration(p.Auth.SessionDuration)
	if err != nil {
		return 24 * time.Hour
	}
	return d
}
