// Package runtime provides platform abstraction for service lifecycle operations.
// It detects the runtime environment (systemd, Docker, etc.) and provides
// the appropriate implementation for restarting services and locating binaries.
package runtime

import (
	"log/slog"
	"os"
	"os/exec"

	"tracer/build"
	"tracer/libs/shared/install"
)

// Slug identifies a tracer service for lifecycle operations.
type Slug = string

const (
	SlugPanel     Slug = "tracer-panel"
	SlugCollector Slug = "tracer-collector"
)

// Manager handles service lifecycle operations.
type Manager interface {
	// Restart restarts the service. In Docker/exec mode this exits the
	// current process so the container orchestrator can restart it.
	Restart() error

	// BinaryDir returns the canonical directory where binaries are installed.
	BinaryDir() string

	// Slug returns the service slug this manager was created for.
	Slug() string
}

// systemdManager implements Manager via systemctl.
type systemdManager struct {
	slug string
}

func (m *systemdManager) Restart() error {
	return exec.Command("systemctl", "restart", m.slug).Run()
}

func (m *systemdManager) BinaryDir() string {
	return "/usr/local/bin"
}

func (m *systemdManager) Slug() string { return m.slug }

// dockerManager implements Manager for container environments (Docker, k8s, etc).
// Restart logs and exits — the container orchestrator handles the restart.
type dockerManager struct {
	slug string
}

func (m *dockerManager) Restart() error {
	slog.Info("Binary updated; exiting for container/process manager to restart",
		"service", m.slug)
	os.Exit(0)
	panic("unreachable")
}

func (m *dockerManager) BinaryDir() string {
	return build.Bindir
}

func (m *dockerManager) Slug() string { return m.slug }

// selectedManager returns the appropriate manager implementation based on
// build-time configuration and runtime detection.
func selectedManager() (systemd bool) {
	switch build.WithSystemd {
	case "yes":
		return true
	case "no":
		return false
	default: // "auto"
		return install.SystemdDetected()
	}
}

// NewManager creates a new Manager for the given service slug.
// The manager implementation is selected based on build-time configuration
// and runtime detection.
func NewManager(slug Slug) Manager {
	if selectedManager() {
		slog.Debug("runtime: using systemd manager", "slug", slug)
		return &systemdManager{slug: slug}
	}
	slog.Debug("runtime: using docker/exec manager", "slug", slug)
	return &dockerManager{slug: slug}
}
