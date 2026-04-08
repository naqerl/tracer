// Package install provides self-installation for tracer-panel.
package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"

	"tracer/apps/panel/internal/cli"
	"tracer/apps/panel/internal/config"
)

const (
	ConfigDir     = "/etc/tracer"
	DataDir       = "/var/lib/tracer"
	ConfigPathEnv = "TRACER_PANEL_CONFIG"
	User          = "tracer"
)

var (
	ConfigFilePath = ConfigDir + "/panel.yaml"
	ConfigEnvVar   = ConfigPathEnv
)

func Run() error {
	// All logs go to stderr, only API key goes to stdout
	fmt.Fprintln(os.Stderr, "Setting up tracer-panel...")

	if err := createUser(); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if err := createDirs(); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}
	if err := createConfig(); err != nil {
		return fmt.Errorf("create config: %w", err)
	}

	// Install systemd service if enabled at compile time
	if err := maybeInstallSystemd(); err != nil {
		return fmt.Errorf("install systemd: %w", err)
	}

	// Initialize panel and get API key
	apiKey, err := initializeAndGetAPIKey()
	if err != nil {
		return fmt.Errorf("initialize panel: %w", err)
	}

	// Output only API key to stdout
	if apiKey != "" {
		fmt.Println(apiKey)
	}

	return nil
}

// initializeAndGetAPIKey runs panel initialization and returns the collector API key.
// It uses TRACER_ADMIN_USERNAME and TRACER_ADMIN_PASSWORD env vars if available,
// otherwise auto-generates admin credentials.
// Returns an error if the panel is already initialized (to avoid overwriting existing API keys).
func initializeAndGetAPIKey() (string, error) {
	// Check if host is configured
	cfg := config.Load(ConfigFilePath)
	if cfg.Host == "" {
		return "", fmt.Errorf("panel host not configured")
	}

	fmt.Fprintln(os.Stderr, "Initializing panel...")

	ctx := context.Background()

	// Try to initialize - env vars are handled internally by cli.InitPanel
	result, err := cli.InitPanel(ctx, cli.InitOptions{})
	if err != nil {
		if errors.Is(err, cli.ErrAlreadyInitialized) {
			return "", fmt.Errorf("panel already initialized")
		}
		return "", fmt.Errorf("initialize panel: %w", err)
	}

	// New initialization succeeded
	if result.IsNewPassword {
		fmt.Fprintf(os.Stderr, "Admin password: %s\n", result.AdminPassword)
	}

	if err := fixDBPermissions(); err != nil {
		return "", fmt.Errorf("fix database permissions: %w", err)
	}

	return result.CollectorAPIKey, nil
}

// fixDBPermissions ensures the database files are owned by the panel user.
func fixDBPermissions() error {
	dataDir := config.DataDir()
	dbPath := dataDir + "/panel.db"

	// Check if database exists
	if _, err := os.Stat(dbPath); err != nil {
		return nil // Database doesn't exist, nothing to fix
	}

	// Chown all panel.db files
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "panel.db") {
			path := dataDir + "/" + entry.Name()
			if err := os.Chown(path, 0, 0); err != nil {
				// If we can't chown, just warn - the service might still work if permissions are OK
				fmt.Fprintf(os.Stderr, "  Warning: could not chown %s: %v\n", path, err)
			} else {
				// Change to tracer-panel user/group
				// We need to find the UID/GID for tracer-panel
				cmd := exec.Command("chown", "tracer-panel:tracer-panel", path)
				if err := cmd.Run(); err != nil {
					fmt.Fprintf(os.Stderr, "  Warning: could not chown %s to tracer-panel: %v\n", path, err)
				}
			}
		}
	}

	return nil
}

func createUser() error {
	fmt.Fprintln(os.Stderr, "Creating user tracer-panel...")

	if err := run("groupadd", "-r", User); err != nil {
		if !isExitError(err) {
			return err
		}
		fmt.Fprintf(os.Stderr, "  Group %s already exists\n", User)
	}

	if err := run("useradd", "-r", "-g", User, "-s", "/usr/sbin/nologin", "-d", "/var/lib/tracer", "-c", "Tracer Panel Service", User); err != nil {
		if !isExitError(err) {
			return err
		}
		fmt.Fprintf(os.Stderr, "  User %s already exists\n", User)
	}

	return nil
}

func createDirs() error {
	fmt.Fprintln(os.Stderr, "Creating directories...")

	if err := os.MkdirAll(ConfigDir, 0755); err != nil {
		return fmt.Errorf("could not create config directory %s with %w", ConfigDir, err)
	}
	if err := run("chown", "root:tracer", ConfigDir); err != nil {
		return fmt.Errorf("could not set ownership of %s with %w", ConfigDir, err)
	}

	if err := os.MkdirAll(DataDir, 0750); err != nil {
		return fmt.Errorf("could not create data directory %s with %w", DataDir, err)
	}
	if err := run("chown", "tracer:tracer", DataDir); err != nil {
		return fmt.Errorf("could not set ownership of %s with %w", DataDir, err)
	}

	return nil
}

func createConfig() error {
	if _, err := os.Stat(ConfigFilePath); err == nil {
		fmt.Fprintf(os.Stderr, "Config already exists at %s, skipping\n", ConfigFilePath)
		return nil
	}

	fmt.Fprintf(os.Stderr, "Creating config at %s...\n", ConfigFilePath)

	cfg := config.Default()

	// Set host from environment variable if provided
	if host := os.Getenv("TRACER_PANEL_HOST"); host != "" {
		cfg.Host = host
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(ConfigFilePath, out, 0640); err != nil {
		return err
	}

	return run("chown", "root:tracer", ConfigFilePath)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func isExitError(err error) bool {
	if exitErr, ok := err.(*exec.ExitError); ok {
		_ = exitErr
		return true
	}
	return false
}

// RunConfig prints the value of a configuration key.
// This is used by the "config" subcommand.
func RunConfig(key string) error {
	cfg := config.Load(ConfigFilePath)

	switch key {
	case "host":
		fmt.Println(cfg.Host)
	case "port":
		fmt.Println(cfg.Port)
	case "data_dir":
		fmt.Println(cfg.DataDir)
	case "config_file":
		fmt.Println(ConfigFilePath)
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}

	return nil
}
