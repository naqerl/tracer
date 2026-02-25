package panel

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Panel struct {
	Port          string  `yaml:"port"`
	DataDir       string  `yaml:"data_dir"`
	SampleRate    float64 `yaml:"sample_rate"`
	RetentionDays int     `yaml:"retention_days"`
	PanelURL      string  `yaml:"panel_url"`
}

const (
	panelConfigPathEnv     = "TRACER_PANEL_CONFIG"
	defaultPanelConfigPath = "/etc/tracer/panel.yaml"
)

func Load(path string) (*Panel, error) {
	if path == "" {
		path = os.Getenv(panelConfigPathEnv)
	}
	if path == "" {
		path = defaultPanelConfigPath
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Panel
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
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
