// Package update provides panel self-update capability.
// It encapsulates the update pipeline invariants:
//  1. Build the new panel binary from source
//  2. Verify the built binary is functional
//  3. Install (backup old, copy new, restore on failure)
//  4. Restart the panel service
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

// Service handles panel self-updates.
type Service struct{}

// New creates a new panel update service.
func New() *Service {
	return &Service{}
}

// UpdatePanel performs a panel self-update: builds, verifies, installs, and
// restarts the panel service.
func (s *Service) UpdatePanel() error {
	slog.Info("Panel self-update started")
	mgr := runtime.NewManager(runtime.SlugPanel)
	err := update.Update(update.Options{
		BinaryName: update.BinaryPanel,
		VerifyFn:   verifyPanelBinary,
		PostUpdate: func() error {
			slog.Info("Panel binary updated, restarting service...")
			return mgr.Restart()
		},
	})
	if err != nil {
		slog.Error("Panel update failed", "error", err)
		return err
	}
	return nil
}

// verifyPanelBinary runs a quick sanity check on the newly built panel binary.
func verifyPanelBinary(path string) error {
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
	slog.Info("Binary verification passed", "output", strings.TrimSpace(string(output)))
	return nil
}
