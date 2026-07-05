// Package handler provides event processors for incoming panel messages.
package handler

import (
	"log/slog"

	"tracer/libs/shared/update"
	"tracer/libs/types"
)

// EventProcessor handles events from the panel and manages the update lifecycle.
// Progress reporting is injected after construction via SetProgressReporter
// to break the circular dependency between handler and transport.
type EventProcessor struct {
	updateFn     func(update.ProgressReporter) error
	progReporter update.ProgressReporter
}

// NewEventProcessor creates an EventProcessor.
// updateFn is called when the panel requests a collector self-update.
// The progress reporter starts as a no-op and can be set later via SetProgressReporter.
func NewEventProcessor(updateFn func(update.ProgressReporter) error) *EventProcessor {
	return &EventProcessor{
		updateFn:     updateFn,
		progReporter: func(string, types.ProgressStatus, string) {},
	}
}

// SetProgressReporter sets the callback used to report update progress
// back to the panel over the WebSocket connection. Must be called before
// the first update request arrives, typically right after the transport
// service is created.
func (ep *EventProcessor) SetProgressReporter(r update.ProgressReporter) {
	ep.progReporter = r
}

// HandleEvent processes an incoming event from the panel.
// It satisfies types.EventHandler.
func (ep *EventProcessor) HandleEvent(evt types.Event) {
	switch msg := evt.(type) {
	case types.UpdateRequestedEvent:
		slog.Info("Received update command from panel", "version", msg.Version)
		go func() {
			if err := ep.updateFn(ep.progReporter); err != nil {
				slog.Error("Collector update failed", "error", err)
			}
		}()
	default:
		slog.Debug("Received unknown message type from panel", "type", evt.EventType())
	}
}
