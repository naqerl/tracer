package config

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

type Panel struct {
	Port          string  `yaml:"port"`
	DataDir       string  `yaml:"data_dir"`
	SampleRate    float64 `yaml:"sample_rate"`
	RetentionDays int     `yaml:"retention_days"`
	Host          string  `yaml:"host"`

	// Period delta thresholds: minimum change to create a new period.
	CPUDeltaPercent float64 `yaml:"cpu_delta_percent"`
	MemDeltaKB      int     `yaml:"mem_delta_kb"`
	DiskDeltaKB     int     `yaml:"disk_delta_kb"`
}

// Config wraps Panel with thread-safe access via RWMutex.
// Setters update the in-memory value and persist to disk.
type Config struct {
	mu   sync.RWMutex
	data Panel
	path string
}

const (
	defaultDataDir       = "/var/lib/tracer"
	defaultPort          = "8080"
	defaultSampleRate    = 0.1
	defaultRetentionDays = 90

	defaultCPUDeltaPercent = 1.0
	defaultMemDeltaKB      = 64
	defaultDiskDeltaKB     = 1024
)

func DataDir() string {
	if dir := os.Getenv("TRACER_DATA_DIR"); dir != "" {
		return dir
	}
	return defaultDataDir
}

func Default() Panel {
	return Panel{
		Port:            defaultPort,
		DataDir:         DataDir(),
		SampleRate:      defaultSampleRate,
		RetentionDays:   defaultRetentionDays,
		Host:            "",
		CPUDeltaPercent: defaultCPUDeltaPercent,
		MemDeltaKB:      defaultMemDeltaKB,
		DiskDeltaKB:     defaultDiskDeltaKB,
	}
}

// New creates a Config from a Panel with the given config file path.
func New(data Panel, path string) *Config {
	return &Config{data: data, path: path}
}

func (p Panel) String() (string, error) {
	out, err := yaml.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return string(out), nil
}

func Load(path string) (Panel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Panel{}, fmt.Errorf("read config: %w", err)
	}

	cfg := Default()

	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Panel{}, fmt.Errorf("decode config: %w", err)
	}

	if cfg.Host == "" {
		return Panel{}, errors.New("host is required (e.g., https://tracer.example.com)")
	}

	return cfg, nil
}

// LoadRaw reads and decodes the config file without validation.
// Used by CLI set commands where the config may be incomplete.
func LoadRaw(path string) (Panel, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Panel{}, fmt.Errorf("read config: %w", err)
	}

	cfg := Default()

	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Panel{}, fmt.Errorf("decode config: %w", err)
	}

	return cfg, nil
}

func (p Panel) Persist(path string) error {
	out, err := yaml.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, out, 0644)
}

// --- Getters ---

func (c *Config) GetPort() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.Port
}

func (c *Config) GetDataDir() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.DataDir
}

func (c *Config) GetHost() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.Host
}

func (c *Config) GetSampleRate() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.SampleRate
}

func (c *Config) GetRetentionDays() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.RetentionDays
}

func (c *Config) GetCPUDeltaPercent() float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.CPUDeltaPercent
}

func (c *Config) GetMemDeltaKB() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.MemDeltaKB
}

func (c *Config) GetDiskDeltaKB() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.data.DiskDeltaKB
}

// --- Setters (update in-memory + persist to disk) ---

func (c *Config) SetSampleRate(v float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.SampleRate = v
	c.persist()
}

func (c *Config) SetRetentionDays(v int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.RetentionDays = v
	c.persist()
}

func (c *Config) SetCPUDeltaPercent(v float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.CPUDeltaPercent = v
	c.persist()
}

func (c *Config) SetMemDeltaKB(v int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.MemDeltaKB = v
	c.persist()
}

func (c *Config) SetDiskDeltaKB(v int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.DiskDeltaKB = v
	c.persist()
}

func (c *Config) persist() {
	out, err := yaml.Marshal(c.data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: marshal: %v\n", err)
		return
	}
	if err := os.WriteFile(c.path, out, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "config: write: %v\n", err)
	}
}
