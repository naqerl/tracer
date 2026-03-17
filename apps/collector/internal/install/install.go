// Package install provides self-installation for tracer-collector.
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
	ConfigFilePath = "/etc/tracer/collector.yaml"
	ConfigEnvVar   = "TRACER_CONFIG"
)

//go:embed tracer-collector.service
var serviceTemplate string

var serviceTmpl = template.Must(template.New("service").Parse(serviceTemplate))

// Run executes the install command.
// Flags must be passed in args (e.g., []string{"setup", "-api-key", "xxx"})
func Run(args []string) error {
	// Check for flags in args
	apiKey := ""
	filteredArgs := []string{}

	for i := 0; i < len(args); i++ {
		if args[i] == "-api-key" && i+1 < len(args) {
			apiKey = args[i+1]
			i++ // skip next
		} else {
			filteredArgs = append(filteredArgs, args[i])
		}
	}

	if len(filteredArgs) == 0 {
		return runSetup(apiKey)
	}

	switch filteredArgs[0] {
	case "setup":
		return runSetup(apiKey)
	case "binary":
		return installBinary()
	case "user":
		return createUser()
	case "dirs":
		return createDirs()
	case "config":
		return createConfig(apiKey)
	case "systemd":
		return installSystemd()
	case "metadata":
		return printMetadata()
	default:
		return fmt.Errorf("unknown install command: %s", filteredArgs[0])
	}
}

func runSetup(apiKey string) error {
	fmt.Fprintln(os.Stderr, "Setting up tracer-collector...")

	if err := installBinary(); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	if err := createUser(); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if err := createDirs(); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}
	if err := createConfig(apiKey); err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	if err := installSystemd(); err != nil {
		return fmt.Errorf("install systemd: %w", err)
	}

	fmt.Fprintln(os.Stderr, "\nSetup complete! Start the service with:")
	fmt.Fprintln(os.Stderr, "  sudo systemctl enable --now tracer-collector")
	return nil
}

func installBinary() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	src, err := os.Open(exe)
	if err != nil {
		return err
	}
	defer src.Close()

	dstPath := "/usr/local/bin/tracer-collector"
	fmt.Fprintf(os.Stderr, "Installing binary to %s...\n", dstPath)

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := dst.ReadFrom(src); err != nil {
		return err
	}

	return nil
}

func createUser() error {
	fmt.Fprintln(os.Stderr, "Creating user tracer-collector...")

	if err := run("groupadd", "-r", "tracer-collector"); err != nil {
		if !isExitError(err) {
			return err
		}
		fmt.Fprintln(os.Stderr, "  Group already exists")
	}

	if err := run("useradd", "-r", "-g", "tracer-collector", "-s", "/usr/sbin/nologin", "-d", "/nonexistent", "-c", "Tracer Collector Service", "tracer-collector"); err != nil {
		if !isExitError(err) {
			return err
		}
		fmt.Fprintln(os.Stderr, "  User already exists")
	}

	return nil
}

func createDirs() error {
	fmt.Fprintln(os.Stderr, "Creating directories...")

	if err := os.MkdirAll("/etc/tracer", 0755); err != nil {
		return err
	}

	// Only chown the directory itself, not recursively.
	// The config file will be chowned separately when created.
	return run("chown", "root:tracer-collector", "/etc/tracer")
}

func createConfig(apiKey string) error {
	if _, err := os.Stat(ConfigFilePath); err == nil {
		fmt.Fprintf(os.Stderr, "Config already exists at %s, skipping\n", ConfigFilePath)
		return nil
	}

	fmt.Fprintf(os.Stderr, "Creating config at %s...\n", ConfigFilePath)

	cfg := config.Default()

	// Set API key from parameter or environment variable if provided
	if apiKey != "" {
		cfg.Transport.APIKey = apiKey
	} else if envKey := os.Getenv("TRACER_COLLECTOR_API_KEY"); envKey != "" {
		cfg.Transport.APIKey = envKey
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(ConfigFilePath, out, 0640); err != nil {
		return err
	}

	return run("chown", "root:tracer-collector", ConfigFilePath)
}

func installSystemd() error {
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

func printMetadata() error {
	meta := map[string]any{
		"binary": "tracer-collector",
		"paths": map[string]string{
			"install_dir": "/usr/local/bin",
			"config_dir":  "/etc/tracer",
			"config_file": ConfigFilePath,
		},
		"user": map[string]string{
			"name": "tracer-collector",
		},
	}
	out, _ := yaml.Marshal(meta)
	fmt.Print(string(out))
	return nil
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
