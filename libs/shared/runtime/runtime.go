// Package runtime provides platform abstraction for service lifecycle operations.
// It detects the runtime environment (systemd, Docker, etc.) and provides
// the appropriate implementation for restarting services and locating binaries.
package runtime

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"tracer/build"
)

// Manager handles service lifecycle operations.
// Different implementations exist for systemd, Docker, and testing.
type Manager interface {
	// Restart restarts the named service. In Docker/exec mode this exits the
	// current process so the container orchestrator can restart it.
	Restart(serviceName string) error

	// BinaryDir returns the canonical directory where binaries are installed.
	BinaryDir() string
}

// systemdManager implements Manager via systemctl.
type systemdManager struct{}

func (m *systemdManager) Restart(serviceName string) error {
	return exec.Command("systemctl", "restart", serviceName).Run()
}

func (m *systemdManager) BinaryDir() string {
	return "/usr/local/bin"
}

// dockerManager implements Manager for container environments (Docker, k8s, etc).
// Restart logs and exits — the container orchestrator handles the restart.
type dockerManager struct{}

func (m *dockerManager) Restart(serviceName string) error {
	slog.Info("Binary updated; exiting for container/process manager to restart",
		"service", serviceName)
	os.Exit(0)
	panic("unreachable")
}

func (m *dockerManager) BinaryDir() string {
	return build.Bindir
}

// DefaultManager is the runtime manager selected based on build-time
// configuration and runtime detection.
var DefaultManager Manager

func init() {
	switch build.WithSystemd {
	case "yes":
		DefaultManager = &systemdManager{}
		slog.Debug("runtime: using systemd manager (build-time override)")
	case "no":
		DefaultManager = &dockerManager{}
		slog.Debug("runtime: using docker/exec manager (build-time override)")
	default: // "auto"
		if hasSystemd() {
			DefaultManager = &systemdManager{}
			slog.Debug("runtime: detected systemd, using systemd manager")
		} else {
			DefaultManager = &dockerManager{}
			slog.Debug("runtime: no systemd detected, using docker/exec manager")
		}
	}
}

// hasSystemd checks whether systemd is available and running on this system.
func hasSystemd() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	// Check if systemd is actually the init system (not just installed)
	out, err := exec.Command("systemctl", "is-system-running").Output()
	if err != nil {
		return false
	}
	status := strings.TrimSpace(string(out))
	return status != "offline" && !strings.HasPrefix(status, "off")
}
