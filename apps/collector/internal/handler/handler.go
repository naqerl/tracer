// Package handler provides event processors for incoming panel messages.
package handler

import (
	"log/slog"

	"tracer/apps/collector/internal/transport"
	"tracer/libs/types"
)

// NewEventProcessor creates an EventHandler that processes messages from the panel.
// updateFn is called when the panel requests a collector self-update.
func NewEventProcessor(updateFn func() error) transport.EventHandler {
	return func(evt types.Event) {
		switch msg := evt.(type) {
		case types.UpdateRequestedEvent:
			slog.Info("Received update command from panel", "version", msg.Version)
			go func() {
				if err := updateFn(); err != nil {
					slog.Error("Collector update failed", "error", err)
				}
			}()
		default:
			slog.Debug("Received unknown message type from panel", "type", evt.EventType())
		}
	}
}
