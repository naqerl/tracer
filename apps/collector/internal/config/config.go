package config

import (
	"log"
	"os"

	"github.com/BurntSushi/toml"

	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/stats"
	"tracer/apps/collector/internal/transport"
)

const (
	configPathEnv     = "TRACER_CONFIG"
	defaultConfigPath = "/etc/tracer/config.toml"
	defaultDataDir    = "/usr/lib/tracer/"
)

// Config is the pure TOML configuration representation
type Config struct {
	Transport transport.Spec `toml:"transport"`

	Stats stats.Spec `toml:"stats"`

	Parsers map[string]parser.Spec `toml:"parsers"`
	Apps    []AppSpec              `toml:"apps"`
}

type AppSpec struct {
	Name      string        `toml:"name"`
	ParserID  string        `toml:"parser_id"`
	Source    source.Spec   `toml:"source"`
	Lifecycle LifecycleSpec `toml:"lifecycle"`
}

type LifecycleSpec struct {
	StartPattern   string `toml:"start_pattern"`
	RestartPattern string `toml:"restart_pattern"`
	ExitPattern    string `toml:"exit_pattern"`
}

// Load reads and parses configuration file
func Load(path string) Config {
	if path == "" {
		path = os.Getenv(configPathEnv)
	}
	if path == "" {
		path = defaultConfigPath
	}

	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		log.Fatalf("failed to decode config: %s", err)
	}

	return cfg
}
