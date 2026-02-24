package config

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	configPathEnv        = "TRACER_CONFIG"
	defaultConfigPath    = "/etc/tracer/config.toml"
	defaultDataDir       = "/usr/lib/tracer/"
)

// Config is the pure TOML configuration representation
type Config struct {
	Name             string `toml:"name"`
	PanelHost        string `toml:"panel_host"`
	APIKey           string `toml:"api_key"`

        Stats stats.Spec `toml:"stats"`
        
	Sources []source.Spec `toml:"sources"`
	Parsers []parser.Spec `toml:"parsers"`
	Apps    []app.Spec   `toml:"apps"`
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
