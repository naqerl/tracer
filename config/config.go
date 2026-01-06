package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

const (
	configPathEnv     = "TRACER_CONFIG"
	defaultConfigPath = "/etc/tracer/config.toml"
)

type Collector struct {
	AppName       string    `toml:"app_name"`
	PanelEndpoint string    `toml:"panel_endpoint"`
	LogFiles      []LogFile `toml:"log_files"`
}

type LogFile struct {
	Path    string `toml:"path"`
	Format  string `toml:"format"` // "json", "regex", or "journalctl"
	Pattern string `toml:"pattern,omitempty"`

	// Field mappings for regex named groups or JSON fields
	TimestampField string `toml:"timestamp_field"`
	LevelField     string `toml:"level_field"`
	TraceIDField   string `toml:"trace_id_field"`
	MessageField   string `toml:"message_field"`

	// Journalctl-specific settings
	Unit string `toml:"unit,omitempty"` // systemd unit name for journalctl format
}

func LoadCollector(path string) (*Collector, error) {
	if path == "" {
		path = os.Getenv(configPathEnv)
	}
	if path == "" {
		path = defaultConfigPath
	}

	var cfg Collector
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func (c *Collector) validate() error {
	if c.AppName == "" {
		return fmt.Errorf("app_name is required")
	}
	if c.PanelEndpoint == "" {
		return fmt.Errorf("panel_endpoint is required")
	}
	if len(c.LogFiles) == 0 {
		return fmt.Errorf("at least one log_files entry is required")
	}

	for i, lf := range c.LogFiles {
		if lf.Format != "journalctl" && lf.Path == "" {
			return fmt.Errorf("log_files[%d].path is required", i)
		}
		if lf.Format != "json" && lf.Format != "regex" && lf.Format != "journalctl" {
			return fmt.Errorf("log_files[%d].format must be 'json', 'regex', or 'journalctl'", i)
		}
		if lf.Format == "regex" && lf.Pattern == "" {
			return fmt.Errorf("log_files[%d].pattern is required for regex format", i)
		}
		if lf.Format == "journalctl" && lf.Unit == "" {
			return fmt.Errorf("log_files[%d].unit is required for journalctl format", i)
		}
	}

	return nil
}

// Panel configuration
type Panel struct {
	Port           string   `toml:"port"`
	DataDir        string   `toml:"data_dir"`
	SampleRate     float64  `toml:"sample_rate"`
	RetentionDays  int      `toml:"retention_days"`
	TelegramToken  string   `toml:"telegram_token"`
	TelegramChatID int64    `toml:"telegram_chat_id"`
	Telegram       Telegram `toml:"telegram"`
}

type Telegram struct {
	Token  string `toml:"token"`
	ChatID int64  `toml:"chat_id"`
}

const (
	panelConfigPathEnv     = "TRACER_PANEL_CONFIG"
	defaultPanelConfigPath = "/etc/tracer/panel.toml"
)

func LoadPanel(path string) (*Panel, error) {
	if path == "" {
		path = os.Getenv(panelConfigPathEnv)
	}
	if path == "" {
		path = defaultPanelConfigPath
	}

	var cfg Panel
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config: %w", err)
	}

	// Apply defaults
	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "/var/lib/tracer"
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 0.1 // 10% of non-error logs
	}
	if cfg.RetentionDays == 0 {
		cfg.RetentionDays = 90 // 3 months default
	}

	// Support both nested and flat telegram config
	if cfg.Telegram.Token != "" {
		cfg.TelegramToken = cfg.Telegram.Token
		cfg.TelegramChatID = cfg.Telegram.ChatID
	}

	return &cfg, nil
}
