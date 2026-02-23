package panel

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

type Panel struct {
	Port          string  `toml:"port"`
	DataDir       string  `toml:"data_dir"`
	SampleRate    float64 `toml:"sample_rate"`
	RetentionDays int     `toml:"retention_days"`
	PanelURL      string  `toml:"panel_url"`
}

const (
	panelConfigPathEnv     = "TRACER_PANEL_CONFIG"
	defaultPanelConfigPath = "/etc/tracer/panel.toml"
)

func Load(path string) (*Panel, error) {
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

	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/tracer"
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 0.1
	}
	if cfg.RetentionDays == 0 {
		cfg.RetentionDays = 90
	}

	return &cfg, nil
}
