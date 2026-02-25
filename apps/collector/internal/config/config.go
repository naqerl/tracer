package config

import (
	"log"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/ai-shift/tracer/apps/collector/internal/parser"
	"github.com/ai-shift/tracer/apps/collector/internal/source"
	"github.com/ai-shift/tracer/apps/collector/internal/stats"
	"github.com/ai-shift/tracer/apps/collector/internal/transport"
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
	Name     string      `toml:"name"`
	ParserID string      `toml:"parser_id"`
	Source   source.Spec `toml:"source"`
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
