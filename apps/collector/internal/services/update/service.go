// Package update provides collector self-update capability.
// It encapsulates the full update pipeline: building the new binary
// from source, verifying it is functional, installing it with rollback
// support, and restarting the service.
package update

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"tracer/libs/shared/runtime"
	"tracer/libs/shared/update"
)

// Service handles collector self-updates.
type Service struct {
	mgr runtime.Manager
}

// New creates a new collector update service.
func New() *Service {
	return &Service{
		mgr: runtime.NewManager(runtime.SlugCollector),
	}
}

// Run performs the full collector update pipeline: build, verify, install, and
// restart. It is safe to call concurrently — the underlying update.Update is
// sequential internally.
func (s *Service) Run() error {
	slog.Info("Collector self-update started")
	err := update.Update(update.Options{
		BinaryName: update.BinaryCollector,
		VerifyFn:   s.verify,
		PostUpdate: func() error {
			slog.Info("Collector binary updated, restarting service...")
			return s.mgr.Restart()
		},
	})
	if err != nil {
		slog.Error("Collector update failed", "error", err)
		return err
	}
	return nil
}

// verify checks the built binary is functional before installing.
func (s *Service) verify(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--version")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("binary verification failed (exit: %w, output: %s)", err, string(output))
	}
	if len(output) == 0 {
		return fmt.Errorf("binary produced no output for --version")
	}
	fmt.Printf("Verified binary: %s\n", strings.TrimSpace(string(output)))
	return nil
}
