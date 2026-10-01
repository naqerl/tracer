package install

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"text/template"

	"gopkg.in/yaml.v3"

	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/kubeconfig"
	"tracer/build"
	"tracer/libs/installx"
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
	Kubeconfig     string
}

func Run() error {
	fmt.Fprintln(os.Stderr, "Setting up tracer-collector...")

	if err := installx.CreateGroup(build.TracerGroup); err != nil {
		return fmt.Errorf("create group: %w", err)
	}
	if err := installx.CreateSystemUser(build.CollectorUser, build.TracerGroup, "Tracer Collector Service", "/nonexistent"); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	if err := installx.CreateDir(ConfigDir, 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := installx.ChownDir(ConfigDir, "root", build.TracerGroup); err != nil {
		return fmt.Errorf("chown config dir: %w", err)
	}
	if err := createConfig(); err != nil {
		return fmt.Errorf("create config: %w", err)
	}
	kubeconfigPath := ensureKubeconfig()
	if err := InstallSystemd(kubeconfigPath); err != nil {
		return fmt.Errorf("install systemd: %w", err)
	}

	fmt.Fprintln(os.Stderr, "\nSetup complete!")
	return nil
}

// ensureKubeconfig installs a kubeconfig the collector service user can read
// so kubectl log sources work without a home directory. A host without a
// cluster just gets an empty result.
func ensureKubeconfig() string {
	if !kubectlAvailable() {
		return ""
	}
	path, err := kubeconfig.Ensure(os.Getenv("TRACER_KUBECONFIG"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Could not install a kubeconfig, kubectl sources will not work: %v\n", err)
		return ""
	}
	fmt.Fprintf(os.Stderr, "Kubectl sources will use %s\n", path)
	return path
}

func kubectlAvailable() bool {
	_, err := exec.LookPath("kubectl")
	return err == nil
}

// InstallSystemd writes the collector unit, pointing kubectl at the given
// kubeconfig when one is installed. Passing an empty path leaves KUBECONFIG
// unset so the daemon falls back to the ambient environment.
func InstallSystemd(kubeconfigPath string) error {
	var buf bytes.Buffer
	data := serviceData{
		ConfigFilePath: ConfigFilePath,
		Bindir:         build.Bindir,
		User:           build.CollectorUser,
		Group:          build.TracerGroup,
		Kubeconfig:     kubeconfigPath,
	}
	if err := serviceTmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}
	return installx.InstallService("/etc/systemd/system/tracer-collector.service", buf.Bytes())
}

func createConfig() error {
	if installx.FileExists(ConfigFilePath) {
		fmt.Fprintf(os.Stderr, "Config already exists at %s, skipping\n", ConfigFilePath)
		return nil
	}

	cfg := config.Config{}
	if envKey := os.Getenv("TRACER_COLLECTOR_API_KEY"); envKey != "" {
		cfg.Sender.APIKey = envKey
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// The config is owned by the collector service user so the daemon can
	// manage it; root and the tracer group keep read access via 0640.
	return installx.WriteConfig(ConfigFilePath, out, 0640, build.CollectorUser, build.TracerGroup)
}
