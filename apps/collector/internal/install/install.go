// Package install provides self-installation for tracer-collector.
// Note: This assumes the binary is already installed (via make install).
// It only handles user, directories, config, and systemd setup.
package install

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"tracer/apps/collector/internal/config"
)

// Public constants for config path - single source of truth
const (
	ConfigDir      = "/etc/tracer"
	ConfigFilePath = ConfigDir + "/collector.yaml"
	ConfigEnvVar   = "TRACER_CONFIG"
	User           = "tracer-collector"
)

//go:embed tracer-collector.service
var serviceTemplate string

var serviceTmpl = template.Must(template.New("service").Parse(serviceTemplate))

// Run executes the install command.
// This performs full setup (user, dirs, config, systemd).
func Run() error {
	fmt.Fprintln(os.Stderr, "Setting up tracer-collector...")

	if err := createUser(); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if err := createDirs(); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}
	if err := createConfig(); err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	if err := maybeInstallSystemd(); err != nil {
		return fmt.Errorf("install systemd: %w", err)
	}

	fmt.Fprintln(os.Stderr, "\nSetup complete!")
	return nil
}

func createUser() error {
	fmt.Fprintf(os.Stderr, "Creating user %s...\n", User)

	if err := run("groupadd", "-r", User); err != nil {
		if !isExitError(err) {
			return err
		}
		fmt.Fprintf(os.Stderr, "  Group %s already exists\n", User)
	}

	if err := run("useradd", "-r", "-g", User, "-s", "/usr/sbin/nologin", "-d", "/nonexistent", "-c", "Tracer Collector Service", User); err != nil {
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
		return fmt.Errorf("create config dir: %w", err)
	}

	// Only chown the directory itself, not recursively.
	// The config file will be chowned separately when created.
	if err := run("chown", "root:"+User, ConfigDir); err != nil {
		return fmt.Errorf("chown config dir: %w", err)
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

	// Set API key from environment variable if provided
	if envKey := os.Getenv("TRACER_COLLECTOR_API_KEY"); envKey != "" {
		cfg.Transport.APIKey = envKey
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(ConfigFilePath, out, 0640); err != nil {
		return err
	}

	return run("chown", "root:"+User, ConfigFilePath)
}

// maybeInstallSystemd installs systemd service if systemd is available.
func maybeInstallSystemd() error {
	// Check if systemd is running
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		fmt.Fprintln(os.Stderr, "Systemd not detected, skipping service installation")
		return nil
	}

	fmt.Fprintln(os.Stderr, "Installing systemd service...")

	var buf strings.Builder
	data := struct {
		ConfigFilePath string
	}{
		ConfigFilePath: ConfigFilePath,
	}
	if err := serviceTmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}

	if err := os.WriteFile("/etc/systemd/system/tracer-collector.service", []byte(buf.String()), 0644); err != nil {
		return err
	}

	return run("systemctl", "daemon-reload")
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
