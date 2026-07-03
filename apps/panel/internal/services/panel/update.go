package panel

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"tracer/apps/panel/internal/services/ingest"
	"tracer/libs/shared/runtime"
	"tracer/libs/shared/update"
)

// NotifyCollectors sends an update command to all connected collectors and
// returns the number of collectors notified.
func (s *Service) NotifyCollectors(connManager *ingest.ConnManager) int {
	if connManager == nil {
		return 0
	}
	return connManager.SendUpdate()
}

// UpdatePanel performs a panel self-update: builds, verifies, installs, and
// restarts the panel service. It is designed to be called in a background
// goroutine after responding to the HTTP request.
func (s *Service) UpdatePanel() {
	slog.Info("Running panel self-update...")
	if err := update.Update(update.Options{
		BinaryName: update.BinaryPanel,
		VerifyFn:   verifyPanelBinary,
		PostUpdate: func() error {
			slog.Info("Panel binary updated, restarting service...")
			return runtime.NewManager(runtime.SlugPanel).Restart()
		},
	}); err != nil {
		slog.Error("Panel update failed", "error", err)
	}
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
