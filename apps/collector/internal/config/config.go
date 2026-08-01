package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"tracer/apps/collector/internal/health"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/sender"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/stats"
	"tracer/build"
	"tracer/libs/installx"
)

type Config struct {
	Sender sender.Spec `yaml:"sender"`

	Stats stats.Spec `yaml:"stats"`

	Parsers map[string]parser.Spec `yaml:"parsers"`
	Apps    []AppSpec              `yaml:"apps"`
}

type AppSpec struct {
	Name      string        `yaml:"name"`
	ParserID  string        `yaml:"parser_id"`
	Source    source.Spec   `yaml:"source"`
	Lifecycle LifecycleSpec `yaml:"lifecycle"`
	Health    *health.Spec  `yaml:"health"`
}

type LifecycleSpec struct {
	StartPattern   string `yaml:"start_pattern"`
	RestartPattern string `yaml:"restart_pattern"`
	ExitPattern    string `yaml:"exit_pattern"`
}

func Default() Config {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "my-collector"
	}

	return Config{
		Sender: sender.Spec{
			Name:      hostname,
			PanelHost: "localhost:8080",
			APIKey:    "",
		},
		Stats: stats.Spec{
			StatsInterval: 30 * time.Second,
		},
		Parsers: map[string]parser.Spec{},
		Apps:    []AppSpec{},
	}
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

	cfg := Default()

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
