// Package handler provides event processors for incoming panel messages.
package handler

import (
	"log/slog"

	"tracer/libs/shared/update"
	"tracer/libs/types"
)

// ProgressSender is called during a self-update to report progress back to the panel.
type ProgressSender update.ProgressReporter

// NewEventProcessor creates an EventHandler that processes messages from the panel.
// updateFn is called when the panel requests a collector self-update.
// If sendProgress is non-nil, it will be called with each stage of the update.
func NewEventProcessor(updateFn func(update.ProgressReporter) error, sendProgress update.ProgressReporter) types.EventHandler {
	return func(evt types.Event) {
		switch msg := evt.(type) {
		case types.UpdateRequestedEvent:
			slog.Info("Received update command from panel", "version", msg.Version)
			go func() {
				// Create a combined reporter: if one is provided externally, use it.
				// Otherwise try to send progress through the provided sendProgress.
				reporter := sendProgress
				if reporter == nil {
					reporter = func(stage string, status types.ProgressStatus, message string) {}
				}
				if err := updateFn(reporter); err != nil {
					slog.Error("Collector update failed", "error", err)
				}
			}()
		default:
			slog.Debug("Received unknown message type from panel", "type", evt.EventType())
		}
	}
}
