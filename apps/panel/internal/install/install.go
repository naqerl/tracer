// Package install provides self-installation for tracer-panel.
package install

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"

	"tracer/apps/panel/internal/config"
)

// Public constants for config path - single source of truth
const (
	ConfigFilePath = "/etc/tracer/panel.yaml"
	ConfigEnvVar   = "TRACER_PANEL_CONFIG"
)

//go:embed tracer-panel.service
var serviceTemplate string

var serviceTmpl = template.Must(template.New("service").Parse(serviceTemplate))

// Run executes the install command.
func Run(args []string) error {
	if len(args) == 0 {
		return runSetup()
	}

	switch args[0] {
	case "setup":
		return runSetup()
	case "binary":
		return installBinary()
	case "user":
		return createUser()
	case "dirs":
		return createDirs()
	case "config":
		return createConfig()
	case "systemd":
		return installSystemd()
	case "metadata":
		return printMetadata()
	default:
		return fmt.Errorf("unknown install command: %s", args[0])
	}
}

// RunConfig outputs a config value by key.
// Supported keys: host
func RunConfig(key string) error {
	cfg := config.Load(ConfigFilePath)

	switch key {
	case "host":
		fmt.Println(cfg.Host)
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}

	return nil
}

func runSetup() error {
	// All logs go to stderr, only API key goes to stdout
	fmt.Fprintln(os.Stderr, "Setting up tracer-panel...")

	if err := installBinary(); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	if err := createUser(); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if err := createDirs(); err != nil {
		return fmt.Errorf("create directories: %w", err)
	}
	if err := createConfig(); err != nil {
		return fmt.Errorf("create config: %w", err)
	}

	// Always initialize panel and get API key
	apiKey, err := initializeAndGetAPIKey()
	if err != nil {
		return fmt.Errorf("initialize panel: %w", err)
	}

	if err := installSystemd(); err != nil {
		return fmt.Errorf("install systemd: %w", err)
	}

	fmt.Fprintln(os.Stderr, "\nSetup complete! Start the service with:")
	fmt.Fprintln(os.Stderr, "  sudo systemctl enable --now tracer-panel")

	// Output only API key to stdout
	if apiKey != "" {
		fmt.Println(apiKey)
	}

	return nil
}

// initializeAndGetAPIKey runs panel initialization and returns the collector API key.
// It uses TRACER_ADMIN_USERNAME and TRACER_ADMIN_PASSWORD env vars if available,
// otherwise auto-generates admin credentials.
func initializeAndGetAPIKey() (string, error) {
	// Check if host is configured
	cfg := config.Load(ConfigFilePath)
	if cfg.Host == "" {
		return "", fmt.Errorf("panel host not configured")
	}

	// Get credentials from env vars or auto-generate
	adminUser := os.Getenv("TRACER_ADMIN_USERNAME")
	adminPass := os.Getenv("TRACER_ADMIN_PASSWORD")

	if adminUser == "" {
		adminUser = "admin"
	}
	if adminPass == "" {
		var err error
		adminPass, err = generateRandomPassword()
		if err != nil {
			return "", fmt.Errorf("generate admin password: %w", err)
		}
	}

	fmt.Fprintln(os.Stderr, "Initializing panel...")

	// Run init command
	cmd := exec.Command("/usr/local/bin/tracer-panel", "-config", ConfigFilePath, "-init", "-username", adminUser, "-password", adminPass)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if already initialized - if so, just generate a new API key
		if strings.Contains(string(output), "already initialized") {
			fmt.Fprintln(os.Stderr, "  Panel already initialized, generating new API key...")
			cmd = exec.Command("/usr/local/bin/tracer-panel", "-config", ConfigFilePath, "-generate-api-key")
			keyOutput, keyErr := cmd.CombinedOutput()
			if keyErr != nil {
				return "", fmt.Errorf("generate api key failed: %w\nOutput: %s", keyErr, string(keyOutput))
			}
			return strings.TrimSpace(string(keyOutput)), nil
		}
		return "", fmt.Errorf("init failed: %w\nOutput: %s", err, string(output))
	}

	// Parse API key from output
	outputStr := string(output)
	for _, line := range strings.Split(outputStr, "\n") {
		if strings.HasPrefix(line, "collector_api_key=") {
			apiKey := strings.TrimPrefix(line, "collector_api_key=")
			// Fix database permissions - init runs as root but panel runs as tracer-panel
			if err := fixDBPermissions(); err != nil {
				return "", fmt.Errorf("fix database permissions: %w", err)
			}
			return apiKey, nil
		}
	}

	// Fix database permissions even if we didn't find the key
	if err := fixDBPermissions(); err != nil {
		return "", fmt.Errorf("fix database permissions: %w", err)
	}

	return "", fmt.Errorf("api key not found in init output")
}

// generateRandomPassword generates a random password for the admin user.
func generateRandomPassword() (string, error) {
	// Use openssl to generate a random password if available
	cmd := exec.Command("openssl", "rand", "-base64", "16")
	output, err := cmd.Output()
	if err != nil {
		// Fallback: generate a simple random string
		b := make([]byte, 16)
		for i := range b {
			b[i] = byte(65 + (i % 26)) // A-Z
		}
		return string(b), nil
	}
	return strings.TrimSpace(string(output)), nil
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

	dstPath := "/usr/local/bin/tracer-panel"
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
	fmt.Fprintln(os.Stderr, "Creating user tracer-panel...")

	if err := run("groupadd", "-r", "tracer-panel"); err != nil {
		if !isExitError(err) {
			return err
		}
		fmt.Fprintln(os.Stderr, "  Group already exists")
	}

	if err := run("useradd", "-r", "-g", "tracer-panel", "-s", "/usr/sbin/nologin", "-d", "/var/lib/tracer", "-c", "Tracer Panel Service", "tracer-panel"); err != nil {
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
	if err := os.MkdirAll("/var/lib/tracer", 0750); err != nil {
		return err
	}

	// Only chown the directories themselves, not recursively.
	// The config file will be chowned separately when created.
	if err := run("chown", "root:tracer-panel", "/etc/tracer"); err != nil {
		return err
	}

	return run("chown", "tracer-panel:tracer-panel", "/var/lib/tracer")
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

	return run("chown", "root:tracer-panel", ConfigFilePath)
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

	if err := os.WriteFile("/etc/systemd/system/tracer-panel.service", []byte(buf.String()), 0644); err != nil {
		return err
	}

	return run("systemctl", "daemon-reload")
}

func printMetadata() error {
	meta := map[string]any{
		"binary": "tracer-panel",
		"paths": map[string]string{
			"install_dir": "/usr/local/bin",
			"config_dir":  "/etc/tracer",
			"config_file": ConfigFilePath,
			"data_dir":    "/var/lib/tracer",
		},
		"user": map[string]string{
			"name": "tracer-panel",
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
