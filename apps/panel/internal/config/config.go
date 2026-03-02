package config

import (
	"fmt"
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
	defaultDataDir         = "/var/lib/tracer"
	defaultPort            = "8080"
	defaultSampleRate      = 0.1
	defaultRetentionDays   = 90
)

// DataDir returns the data directory, checking env var first
func DataDir() string {
	if dir := os.Getenv("TRACER_DATA_DIR"); dir != "" {
		return dir
	}
	return defaultDataDir
}

// Default returns the default panel configuration
func Default() Panel {
	return Panel{
		Port:          defaultPort,
		DataDir:       DataDir(),
		SampleRate:    defaultSampleRate,
		RetentionDays: defaultRetentionDays,
		Host:          "",
	}
}

// String returns the YAML representation of the configuration
func (p Panel) String() (string, error) {
	out, err := yaml.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return string(out), nil
}

// Load reads and parses configuration file, overlaying on top of defaults
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

	// Start with defaults
	cfg := Default()

	// Overlay with file contents
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		log.Fatalf("failed to decode config: %s", err)
	}

	if cfg.Host == "" {
		log.Fatalf("host is required (e.g., https://tracer.example.com)")
	}

	return cfg
}
