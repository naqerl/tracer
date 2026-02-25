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

	Sources []source.Spec `toml:"sources"`
	Parsers []parser.Spec `toml:"parsers"`
	Apps    []AppRef      `toml:"apps"`
}

type AppRef struct {
	Name     string `toml:"name"`
	SourceID string `toml:"source_id"`
	ParserID string `toml:"parser_id"`
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
