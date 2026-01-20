package collector

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

const (
	configPathEnv     = "TRACER_CONFIG"
	defaultConfigPath = "/etc/tracer/config.toml"
)

type Collector struct {
	Name             string  `toml:"name"`
	PanelEndpoint    string  `toml:"panel_endpoint"`
	StatsInterval    string  `toml:"stats_interval"`
	DataDir          string  `toml:"data_dir"`
	CPUAlertPercent  float64 `toml:"cpu_alert_percent"`
	MemAlertPercent  float64 `toml:"mem_alert_percent"`
	DiskAlertPercent float64 `toml:"disk_alert_percent"`
	Apps             []App   `toml:"apps"`
}

type App struct {
	Name    string `toml:"name"`
	Path    string `toml:"path"`
	Format  string `toml:"format"`
	Pattern string `toml:"pattern,omitempty"`

	TimestampField  string `toml:"timestamp_field"`
	TimestampFormat string `toml:"timestamp_format"`
	LevelField      string `toml:"level_field"`
	TraceIDField    string `toml:"trace_id_field"`
	MessageField    string `toml:"message_field"`

	Unit string `toml:"unit,omitempty"`

	StartupPattern string `toml:"startup_pattern,omitempty"`
}

func Load(path string) (*Collector, error) {
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
		if app.Format != "journalctl" && app.TimestampFormat == "" {
			return fmt.Errorf("apps[%d].timestamp_format is required for %s format", i, app.Format)
		}
		if app.Format == "journalctl" && app.Unit == "" {
			return fmt.Errorf("apps[%d].unit is required for journalctl format", i)
		}
	}

	return nil
}
