package config

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
	AppName       string    `toml:"app_name"`
	PanelEndpoint string    `toml:"panel_endpoint"`
	LogFiles      []LogFile `toml:"log_files"`
}

type LogFile struct {
	Path    string `toml:"path"`
	Format  string `toml:"format"` // "json" or "regex"
	Pattern string `toml:"pattern,omitempty"`

	// Field mappings for regex named groups or JSON fields
	TimestampField string `toml:"timestamp_field"`
	LevelField     string `toml:"level_field"`
	TraceIDField   string `toml:"trace_id_field"`
	MessageField   string `toml:"message_field"`
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
	if c.AppName == "" {
		return fmt.Errorf("app_name is required")
	}
	if c.PanelEndpoint == "" {
		return fmt.Errorf("panel_endpoint is required")
	}
	if len(c.LogFiles) == 0 {
		return fmt.Errorf("at least one log_files entry is required")
	}

	for i, lf := range c.LogFiles {
		if lf.Path == "" {
			return fmt.Errorf("log_files[%d].path is required", i)
		}
		if lf.Format != "json" && lf.Format != "regex" {
			return fmt.Errorf("log_files[%d].format must be 'json' or 'regex'", i)
		}
		if lf.Format == "regex" && lf.Pattern == "" {
			return fmt.Errorf("log_files[%d].pattern is required for regex format", i)
		}
	}

	return nil
}
