// Package update provides the panel-side update service, orchestrating
// notifications to connected collectors and triggering the panel self-update.
package update

import (
	"fmt"
	"log/slog"

	"tracer/apps/panel/internal/services/ingest"
	"tracer/libs/shared/runtime"
	"tracer/libs/shared/update"
)

// UpdateResult describes the outcome of an update-all request.
type UpdateResult struct {
	CollectorsNotified int    `json:"collectors_notified"`
	Message            string `json:"message"`
}

// Service handles update operations for the panel.
type Service struct {
	connManager *ingest.ConnManager
}

// New creates a new update Service.
func New(connManager *ingest.ConnManager) *Service {
	return &Service{
		connManager: connManager,
	}
}

// UpdateAll sends update commands to all connected collectors and starts a
// panel self-update in the background. It returns immediately with the number
// of collectors notified.
func (s *Service) UpdateAll() (*UpdateResult, error) {
	collectorCount := s.connManager.SendUpdate()
	slog.Info("Update signal sent to collectors", "count", collectorCount)

	go func() {
		if err := update.SelfUpdate(runtime.SlugPanel); err != nil {
			slog.Error("Panel update failed", "error", err)
		}
	}()

	return &UpdateResult{
		CollectorsNotified: collectorCount,
		Message:            fmt.Sprintf("Update sent to %d collector(s); panel restarting.", collectorCount),
	}, nil
}
