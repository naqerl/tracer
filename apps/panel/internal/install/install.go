package install

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"text/template"

	"gopkg.in/yaml.v3"

	"tracer/apps/panel/internal/config"
	"tracer/build"
	"tracer/libs/installx"
)

const (
	ConfigDir     = "/etc/tracer"
	DataDir       = "/var/lib/tracer"
	ConfigPathEnv = "TRACER_PANEL_CONFIG"
)

var (
	ConfigFilePath = ConfigDir + "/panel.yaml"
	ConfigEnvVar   = ConfigPathEnv
)

//go:embed tracer-panel.service
var serviceTemplate string

var serviceTmpl = template.Must(template.New("service").Parse(serviceTemplate))

type serviceData struct {
	ConfigFilePath string
	Bindir         string
	User           string
	Group          string
}

func Run() error {
	fmt.Fprintln(os.Stderr, "Setting up tracer-panel...")

	if err := installx.CreateGroup(build.TracerGroup); err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	if err := installx.CreateSystemUser(build.PanelUser, build.TracerGroup, "Tracer Panel Service", DataDir); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if err := installx.CreateDir(ConfigDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := installx.ChownDir(ConfigDir, "root", build.TracerGroup); err != nil {
		return fmt.Errorf("chown config dir: %w", err)
	}
	if err := installx.CreateDir(DataDir, 0700); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	if err := installx.ChownDir(DataDir, build.PanelUser, build.TracerGroup); err != nil {
		return fmt.Errorf("chown data dir: %w", err)
	}
	if err := createConfig(); err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	if err := installSystemd(); err != nil {
		return fmt.Errorf("install systemd: %w", err)
	}

	return nil
}

func createConfig() error {
	if installx.FileExists(ConfigFilePath) {
		fmt.Fprintf(os.Stderr, "Config already exists at %s, skipping\n", ConfigFilePath)
		return nil
	}

	cfg := config.Default()
	if host := os.Getenv("TRACER_PANEL_HOST"); host != "" {
		cfg.Host = host
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return installx.WriteConfig(ConfigFilePath, out, 0640, "root", build.TracerGroup)
}

func installSystemd() error {
	var buf bytes.Buffer
	data := serviceData{
		ConfigFilePath: ConfigFilePath,
		Bindir:         build.Bindir,
		User:           build.PanelUser,
		Group:          build.TracerGroup,
	}
	if err := serviceTmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}
	return installx.InstallService("/etc/systemd/system/tracer-panel.service", buf.Bytes())
}

func RunConfig(key string) error {
	cfg, err := config.Load(ConfigFilePath)
	if err != nil {
		return err
	}

	switch key {
	case "host":
		fmt.Println(cfg.Host)
	case "port":
		fmt.Println(cfg.Port)
	case "data_dir":
		fmt.Println(cfg.DataDir)
	case "config_file":
		fmt.Println(ConfigFilePath)
	case "cpu_delta_percent":
		fmt.Printf("%.1f\n", cfg.CPUDeltaPercent)
	case "mem_delta_kb":
		fmt.Println(cfg.MemDeltaKB)
	case "disk_delta_kb":
		fmt.Println(cfg.DiskDeltaKB)
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}

	return nil
}
