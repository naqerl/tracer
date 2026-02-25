package config

import (
	"log"
	"os"

	"gopkg.in/yaml.v3"

	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/stats"
	"tracer/apps/collector/internal/transport"
)

const (
	configPathEnv     = "TRACER_CONFIG"
	defaultConfigPath = "/etc/tracer/collector.yaml"
	defaultDataDir    = "/usr/lib/tracer/"
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
}

type LifecycleSpec struct {
	StartPattern   string `yaml:"start_pattern"`
	RestartPattern string `yaml:"restart_pattern"`
	ExitPattern    string `yaml:"exit_pattern"`
}

// Load reads and parses configuration file
func Load(path string) Config {
	if path == "" {
		path = os.Getenv(configPathEnv)
	}
	if path == "" {
		path = defaultConfigPath
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("failed to read config: %s", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		log.Fatalf("failed to decode config: %s", err)
	}

	return cfg
}
