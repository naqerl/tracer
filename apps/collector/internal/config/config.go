package config

import (
	"fmt"
	"log"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"tracer/apps/collector/internal/health"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/stats"
	"tracer/apps/collector/internal/transport"
)

// Config is the pure YAML configuration representation
type Config struct {
	Transport transport.Spec `yaml:"transport"`

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

// Default returns the default collector configuration
func Default() Config {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "my-collector"
	}

	return Config{
		Transport: transport.Spec{
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

// String returns the YAML representation of the configuration
func (c Config) String() (string, error) {
	out, err := yaml.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return string(out), nil
}

// Load reads and parses configuration file, overlaying on top of defaults.
// The path must be a non-empty string (resolved by caller).
func Load(path string) Config {
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
	return cfg
}

// Persist writes the configuration to the specified path.
// The path must be a non-empty string (resolved by caller).
func (c Config) Persist(path string) error {
	str, err := c.String()
	if err != nil {
		return err
	}

	return os.WriteFile(path, []byte(str), 0644)
}
