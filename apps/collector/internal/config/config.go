package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"tracer/apps/collector/internal/sender"
	"tracer/build"
	"tracer/libs/installx"
)

type Config struct {
	Sender sender.Spec `yaml:"sender"`
}

func (c Config) String() (string, error) {
	out, err := yaml.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return string(out), nil
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	cfg := Config{}

	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	return cfg, nil
}

// Persist writes the config back to disk and hands ownership to the collector
// service user so the daemon can manage its own config. Written 0640 so the
// API key stays readable only by root, the service user, and the tracer group.
func (c Config) Persist(path string) error {
	str, err := c.String()
	if err != nil {
		return err
	}

	if err := os.WriteFile(path, []byte(str), 0640); err != nil {
		return fmt.Errorf("could not write config: %w", err)
	}
	return installx.ChownFile(path, build.CollectorUser, build.TracerGroup)
}
