package config

import (
	"errors"
	"fmt"
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
	defaultDataDir       = "/var/lib/tracer"
	defaultPort          = "8080"
	defaultSampleRate    = 0.1
	defaultRetentionDays = 90
)

func DataDir() string {
	if dir := os.Getenv("TRACER_DATA_DIR"); dir != "" {
		return dir
	}
	return defaultDataDir
}

func Default() Panel {
	return Panel{
		Port:          defaultPort,
		DataDir:       DataDir(),
		SampleRate:    defaultSampleRate,
		RetentionDays: defaultRetentionDays,
		Host:          "",
	}
}

func (p Panel) String() (string, error) {
	out, err := yaml.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return string(out), nil
}

func Load(path string) (Panel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Panel{}, fmt.Errorf("read config: %w", err)
	}

	cfg := Default()

	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Panel{}, fmt.Errorf("decode config: %w", err)
	}

	if cfg.Host == "" {
		return Panel{}, errors.New("host is required (e.g., https://tracer.example.com)")
	}

	return cfg, nil
}
