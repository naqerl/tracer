package install

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"text/template"

	"gopkg.in/yaml.v3"

	"tracer/apps/collector/internal/config"
	"tracer/pkg/setup"
)

const (
	ConfigDir      = "/etc/tracer"
	ConfigFilePath = ConfigDir + "/collector.yaml"
	ConfigEnvVar   = "TRACER_CONFIG"
)

//go:embed tracer-collector.service
var serviceTemplate string

var serviceTmpl = template.Must(template.New("service").Parse(serviceTemplate))

type serviceData struct {
	ConfigFilePath string
	Bindir         string
	User           string
	Group          string
}

func Run() error {
	fmt.Fprintln(os.Stderr, "Setting up tracer-collector...")

	if err := setup.CreateGroup(setup.TracerGroup); err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	if err := setup.CreateSystemUser(setup.CollectorUser, setup.TracerGroup, "Tracer Collector Service", "/nonexistent"); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if err := setup.CreateDir(ConfigDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := setup.ChownDir(ConfigDir, "root", setup.TracerGroup); err != nil {
		return fmt.Errorf("chown config dir: %w", err)
	}
	if err := createConfig(); err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	if err := installSystemd(); err != nil {
		return fmt.Errorf("install systemd: %w", err)
	}

	fmt.Fprintln(os.Stderr, "\nSetup complete!")
	return nil
}

func createConfig() error {
	if setup.FileExists(ConfigFilePath) {
		fmt.Fprintf(os.Stderr, "Config already exists at %s, skipping\n", ConfigFilePath)
		return nil
	}

	cfg := config.Default()
	if envKey := os.Getenv("TRACER_COLLECTOR_API_KEY"); envKey != "" {
		cfg.Transport.APIKey = envKey
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return setup.WriteConfig(ConfigFilePath, out, 0640, "root", setup.TracerGroup)
}

func installSystemd() error {
	var buf bytes.Buffer
	data := serviceData{
		ConfigFilePath: ConfigFilePath,
		Bindir:         setup.Bindir,
		User:           setup.CollectorUser,
		Group:          setup.TracerGroup,
	}
	if err := serviceTmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}
	return setup.InstallService("/etc/systemd/system/tracer-collector.service", buf.Bytes())
}
