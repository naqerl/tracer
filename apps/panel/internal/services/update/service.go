// Package update provides the panel-side update service, orchestrating
// notifications to connected collectors and triggering the panel self-update.
package update

import (
	"fmt"
	"log/slog"
	"time"

	"tracer/apps/panel/internal/services/ingest"
	"tracer/libs/shared/runtime"
	"tracer/libs/shared/update"
	"tracer/libs/types"
)

// UpdateResult describes the outcome of an update-all request.
type UpdateResult struct {
	CollectorsNotified int    `json:"collectors_notified"`
	Message            string `json:"message"`
}

// Service handles update operations for the panel.
type Service struct {
	connManager *ingest.ConnManager
	progTracker *ingest.UpdateProgressTracker
}

// New creates a new update Service.
func New(connManager *ingest.ConnManager, progTracker *ingest.UpdateProgressTracker) *Service {
	return &Service{
		connManager: connManager,
		progTracker: progTracker,
	}
}

// UpdateAll sends update commands to all connected collectors and starts a
// panel self-update in the background. It returns immediately with the number
// of collectors notified.
func (s *Service) UpdateAll() (*UpdateResult, error) {
	// Signal start of update
	s.progTracker.StartUpdate()

	// Notify collectors via WebSocket
	collectorCount := s.connManager.SendUpdate()
	slog.Info("Update signal sent to collectors", "count", collectorCount)

	// Report collector notifications
	if collectorCount > 0 {
		s.progTracker.ReportProgress("collectors", "notify", types.ProgressDone,
			fmt.Sprintf("Notified %d collector(s)", collectorCount))
	} else {
		s.progTracker.ReportProgress("collectors", "notify", types.ProgressDone,
			"No collectors connected")
	}

	// Build a progress reporter that sends progress to the tracker
	panelEntity := "panel"
	progReporter := update.ProgressReporter(func(stage string, status types.ProgressStatus, message string) {
		s.progTracker.ReportProgress(panelEntity, stage, status, message)
	})

	// Run panel self-update in background
	go func() {
		defer s.progTracker.FinishUpdate()
		time.Sleep(100 * time.Millisecond) // brief delay so SSE client can connect
		if err := update.SelfUpdate(runtime.SlugPanel, progReporter); err != nil {
			slog.Error("Panel update failed", "error", err)
			s.progTracker.ReportProgress(panelEntity, "complete", types.ProgressFailed,
				fmt.Sprintf("Update failed: %v", err))
		}
	}()

	return &UpdateResult{
		CollectorsNotified: collectorCount,
		Message:            fmt.Sprintf("Update sent to %d collector(s); panel restarting.", collectorCount),
	}, nil
}
