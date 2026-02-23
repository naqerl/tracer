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
	defaultDataDir       = "/"
	defaultStatsInterval = "30s"
)

type Collector struct {
	Name             string  `toml:"name"`
	PanelEndpoint    string  `toml:"panel_endpoint"`
	APIKey           string  `toml:"api_key"`
	StatsInterval    string  `toml:"stats_interval"`
	DataDir          string  `toml:"data_dir"`
	CPUAlertPercent  float64 `toml:"cpu_alert_percent"`
	MemAlertPercent  float64 `toml:"mem_alert_percent"`
	DiskAlertPercent float64 `toml:"disk_alert_percent"`

	Sources Sources       `toml:"sources"`
	Parsers Parsers       `toml:"parsers"`
	AppRefs []AppRef      `toml:"apps"`
	Apps    []ResolvedApp `toml:"-"`

	// Resolved fields (set during Load with defaults applied)
	ResolvedName          string        `toml:"-"`
	ResolvedDataDir       string        `toml:"-"`
	ResolvedStatsInterval time.Duration `toml:"-"`
}

type Sources struct {
	File       []FileSource       `toml:"file"`
	Journalctl []JournalctlSource `toml:"journalctl"`
}

type FileSource struct {
	ID       string `toml:"id"`
	Path     string `toml:"path"`
	ReadFrom string `toml:"read_from"`
}

type JournalctlSource struct {
	ID    string `toml:"id"`
	Unit  string `toml:"unit"`
	Since string `toml:"since"`
}

type Parsers struct {
	JSON  []JSONParser  `toml:"json"`
	Regex []RegexParser `toml:"regex"`
	Text  []TextParser  `toml:"text"`
}

type JSONParser struct {
	ID              string `toml:"id"`
	TimestampField  string `toml:"timestamp_field"`
	TimestampFormat string `toml:"timestamp_format"`
	LevelField      string `toml:"level_field"`
	TraceIDField    string `toml:"trace_id_field"`
	MessageField    string `toml:"message_field"`
}

type RegexParser struct {
	ID              string `toml:"id"`
	Pattern         string `toml:"pattern"`
	TimestampField  string `toml:"timestamp_field"`
	TimestampFormat string `toml:"timestamp_format"`
	LevelField      string `toml:"level_field"`
	TraceIDField    string `toml:"trace_id_field"`
	MessageField    string `toml:"message_field"`
}

type TextParser struct {
	ID              string `toml:"id"`
	TimestampRegex  string `toml:"timestamp_regex"`
	TimestampFormat string `toml:"timestamp_format"`
	LevelRegex      string `toml:"level_regex"`
	TraceIDRegex    string `toml:"trace_id_regex"`
}

type AppRef struct {
	Name            string `toml:"name"`
	SourceRef       string `toml:"source_ref"`
	ParserRef       string `toml:"parser_ref"`
	StartupPattern  string `toml:"startup_pattern,omitempty"`
	ShutdownPattern string `toml:"shutdown_pattern,omitempty"`
}

type ResolvedApp struct {
	Name            string
	SourceKind      string
	Source          string
	Path            string
	Unit            string
	Since           string
	ReadFrom        string
	ParserKind      string
	Pattern         string
	TimestampField  string
	TimestampFormat string
	LevelField      string
	TraceIDField    string
	MessageField    string
	TimestampRegex  string
	LevelRegex      string
	TraceIDRegex    string
	StartupPattern  string
	ShutdownPattern string
}

func Load(path string) (*Collector, error) {
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

	resolvedApps, err := cfg.resolveApps()
	if err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	cfg.Apps = resolvedApps

	// Apply defaults
	cfg.applyDefaults()

	return &cfg, nil
}

func (c *Collector) validate() error {
	if c.PanelEndpoint == "" {
		return fmt.Errorf("panel_endpoint is required")
	}
	if c.APIKey == "" {
		return fmt.Errorf("api_key is required")
	}

	if len(c.AppRefs) == 0 {
		return fmt.Errorf("at least one [[apps]] entry is required")
	}

	sourceIDs := map[string]string{}

	for i, src := range c.Sources.File {
		if src.ID == "" {
			return fmt.Errorf("sources.file[%d].id is required", i)
		}
		if src.Path == "" {
			return fmt.Errorf("sources.file[%d].path is required", i)
		}
		if prev, exists := sourceIDs[src.ID]; exists {
			return fmt.Errorf("duplicate source id %q in sources.file[%d], already used in %s", src.ID, i, prev)
		}
		sourceIDs[src.ID] = fmt.Sprintf("sources.file[%d]", i)
	}

	for i, src := range c.Sources.Journalctl {
		if src.ID == "" {
			return fmt.Errorf("sources.journalctl[%d].id is required", i)
		}
		if src.Unit == "" {
			return fmt.Errorf("sources.journalctl[%d].unit is required", i)
		}
		if prev, exists := sourceIDs[src.ID]; exists {
			return fmt.Errorf("duplicate source id %q in sources.journalctl[%d], already used in %s", src.ID, i, prev)
		}
		sourceIDs[src.ID] = fmt.Sprintf("sources.journalctl[%d]", i)
	}

	parserIDs := map[string]string{}

	for i, p := range c.Parsers.JSON {
		if p.ID == "" {
			return fmt.Errorf("parsers.json[%d].id is required", i)
		}
		if p.TimestampFormat == "" {
			return fmt.Errorf("parsers.json[%d].timestamp_format is required", i)
		}
		if prev, exists := parserIDs[p.ID]; exists {
			return fmt.Errorf("duplicate parser id %q in parsers.json[%d], already used in %s", p.ID, i, prev)
		}
		parserIDs[p.ID] = fmt.Sprintf("parsers.json[%d]", i)
	}

	for i, p := range c.Parsers.Regex {
		if p.ID == "" {
			return fmt.Errorf("parsers.regex[%d].id is required", i)
		}
		if p.Pattern == "" {
			return fmt.Errorf("parsers.regex[%d].pattern is required", i)
		}
		if p.TimestampFormat == "" {
			return fmt.Errorf("parsers.regex[%d].timestamp_format is required", i)
		}
		if prev, exists := parserIDs[p.ID]; exists {
			return fmt.Errorf("duplicate parser id %q in parsers.regex[%d], already used in %s", p.ID, i, prev)
		}
		parserIDs[p.ID] = fmt.Sprintf("parsers.regex[%d]", i)
	}

	for i, p := range c.Parsers.Text {
		if p.ID == "" {
			return fmt.Errorf("parsers.text[%d].id is required", i)
		}
		if p.TimestampRegex == "" {
			return fmt.Errorf("parsers.text[%d].timestamp_regex is required", i)
		}
		if p.TimestampFormat == "" {
			return fmt.Errorf("parsers.text[%d].timestamp_format is required", i)
		}
		if prev, exists := parserIDs[p.ID]; exists {
			return fmt.Errorf("duplicate parser id %q in parsers.text[%d], already used in %s", p.ID, i, prev)
		}
		parserIDs[p.ID] = fmt.Sprintf("parsers.text[%d]", i)
	}

	for i, app := range c.AppRefs {
		if app.Name == "" {
			return fmt.Errorf("apps[%d].name is required", i)
		}
		if app.SourceRef == "" {
			return fmt.Errorf("apps[%d].source_ref is required", i)
		}
		if app.ParserRef == "" {
			return fmt.Errorf("apps[%d].parser_ref is required", i)
		}
		if _, exists := sourceIDs[app.SourceRef]; !exists {
			return fmt.Errorf("apps[%d].source_ref=%q not found", i, app.SourceRef)
		}
		if _, exists := parserIDs[app.ParserRef]; !exists {
			return fmt.Errorf("apps[%d].parser_ref=%q not found", i, app.ParserRef)
		}
	}

	return nil
}

func (c *Collector) resolveApps() ([]ResolvedApp, error) {
	sources := make(map[string]ResolvedApp, len(c.Sources.File)+len(c.Sources.Journalctl))

	for _, src := range c.Sources.File {
		readFrom := src.ReadFrom
		if readFrom == "" {
			readFrom = "end"
		}
		if readFrom != "start" && readFrom != "end" {
			return nil, fmt.Errorf("sources.file[%s].read_from must be 'start' or 'end'", src.ID)
		}
		sources[src.ID] = ResolvedApp{
			SourceKind: "file",
			Source:     src.Path,
			Path:       src.Path,
			ReadFrom:   readFrom,
		}
	}

	for _, src := range c.Sources.Journalctl {
		since := src.Since
		if since == "" {
			since = "now"
		}
		sources[src.ID] = ResolvedApp{
			SourceKind: "journalctl",
			Source:     "journalctl:" + src.Unit,
			Unit:       src.Unit,
			Since:      since,
		}
	}

	parsers := map[string]ResolvedApp{}

	for _, p := range c.Parsers.JSON {
		parsers[p.ID] = ResolvedApp{
			ParserKind:      "json",
			TimestampField:  p.TimestampField,
			TimestampFormat: p.TimestampFormat,
			LevelField:      p.LevelField,
			TraceIDField:    p.TraceIDField,
			MessageField:    p.MessageField,
		}
	}

	for _, p := range c.Parsers.Regex {
		parsers[p.ID] = ResolvedApp{
			ParserKind:      "regex",
			Pattern:         p.Pattern,
			TimestampField:  p.TimestampField,
			TimestampFormat: p.TimestampFormat,
			LevelField:      p.LevelField,
			TraceIDField:    p.TraceIDField,
			MessageField:    p.MessageField,
		}
	}

	for _, p := range c.Parsers.Text {
		parsers[p.ID] = ResolvedApp{
			ParserKind:      "text",
			TimestampRegex:  p.TimestampRegex,
			TimestampFormat: p.TimestampFormat,
			LevelRegex:      p.LevelRegex,
			TraceIDRegex:    p.TraceIDRegex,
		}
	}

	resolved := make([]ResolvedApp, 0, len(c.AppRefs))
	for _, app := range c.AppRefs {
		src := sources[app.SourceRef]
		prs := parsers[app.ParserRef]

		resolved = append(resolved, ResolvedApp{
			Name:            app.Name,
			SourceKind:      src.SourceKind,
			Source:          src.Source,
			Path:            src.Path,
			Unit:            src.Unit,
			Since:           src.Since,
			ReadFrom:        src.ReadFrom,
			ParserKind:      prs.ParserKind,
			Pattern:         prs.Pattern,
			TimestampField:  prs.TimestampField,
			TimestampFormat: prs.TimestampFormat,
			LevelField:      prs.LevelField,
			TraceIDField:    prs.TraceIDField,
			MessageField:    prs.MessageField,
			TimestampRegex:  prs.TimestampRegex,
			LevelRegex:      prs.LevelRegex,
			TraceIDRegex:    prs.TraceIDRegex,
			StartupPattern:  app.StartupPattern,
			ShutdownPattern: app.ShutdownPattern,
		})
	}

	return resolved, nil
}

func (c *Collector) applyDefaults() {
	// Name: use config value or auto-detect from outbound IP
	if c.Name != "" {
		c.ResolvedName = c.Name
	} else {
		c.ResolvedName = getOutboundIP()
	}

	// DataDir: default to "/"
	if c.DataDir != "" {
		c.ResolvedDataDir = c.DataDir
	} else {
		c.ResolvedDataDir = defaultDataDir
	}

	// StatsInterval: default to 30s, parse config value, 0 to disable
	intervalStr := c.StatsInterval
	if intervalStr == "" {
		intervalStr = defaultStatsInterval
	}
	if intervalStr == "0" {
		c.ResolvedStatsInterval = 0
	} else {
		d, err := time.ParseDuration(intervalStr)
		if err != nil {
			d, _ = time.ParseDuration(defaultStatsInterval)
		}
		c.ResolvedStatsInterval = d
	}
}

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		hostname, _ := os.Hostname()
		if hostname != "" {
			return hostname
		}
		return "unknown"
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}
