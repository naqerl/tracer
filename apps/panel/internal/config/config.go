package config

import (
	"log"
	"os"

	"gopkg.in/yaml.v3"
)

type Panel struct {
	Port          string  `yaml:"port"`
	DataDir       string  `yaml:"data_dir"`
	SampleRate    float64 `yaml:"sample_rate"`
	RetentionDays int     `yaml:"retention_days"`
	Host          string  `yaml:"host"`
}

const (
	panelConfigPathEnv     = "TRACER_PANEL_CONFIG"
	defaultPanelConfigPath = "/etc/tracer/panel.yaml"
)

// Load reads and parses configuration file
func Load(path string) Panel {
	if path == "" {
		path = os.Getenv(panelConfigPathEnv)
	}
	if path == "" {
		path = defaultPanelConfigPath
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("failed to read config: %s", err)
	}

	var cfg Panel
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		log.Fatalf("failed to decode config: %s", err)
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
	if cfg.Host == "" {
		log.Fatalf("host is required (e.g., https://tracer.example.com)")
	}

	return cfg
}
