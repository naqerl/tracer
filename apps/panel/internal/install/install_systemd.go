//go:build systemd

package install

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
	"text/template"
)

//go:embed tracer-panel.service
var serviceTemplate string

var serviceTmpl = template.Must(template.New("service").Parse(serviceTemplate))

// maybeInstallSystemd installs systemd service when build tag is enabled.
// Called from Run() if systemd installation is requested.
func maybeInstallSystemd() error {
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
