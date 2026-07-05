// Package handler provides event processors for incoming panel messages.
package handler

import (
	"log/slog"

	"tracer/libs/shared/update"
	"tracer/libs/types"
)

// EventProcessor handles events from the panel. Update progress is forwarded
// to the transport layer through a channel-based Report from the update
// package, completely decoupling progress reporting from the update logic.
type EventProcessor struct {
	updateFn func() *update.Report
	sendFn   func(string, types.ProgressStatus, string)
}

// NewEventProcessor creates an EventProcessor.
// runUpdate is called to start a self-update and returns a Report whose
// Events channel is forwarded to the transport layer.
func NewEventProcessor(runUpdate func() *update.Report) *EventProcessor {
	return &EventProcessor{
		updateFn: runUpdate,
		sendFn:   func(string, types.ProgressStatus, string) {},
	}
}

// SetProgressSender sets the function used to send progress events back to
// the panel over the WebSocket. Must be called before the first update
// request arrives, typically right after the transport service is created.
func (ep *EventProcessor) SetProgressSender(sendFn func(string, types.ProgressStatus, string)) {
	ep.sendFn = sendFn
}

// HandleEvent processes an incoming event from the panel.
// For UpdateRequestedEvent it starts a self-update and reads progress from
// the Report's Events channel, forwarding each event to sendFn.
func (ep *EventProcessor) HandleEvent(evt types.Event) {
	switch msg := evt.(type) {
	case types.UpdateRequestedEvent:
		slog.Info("Received update command from panel", "version", msg.Version)
		go func() {
			report := ep.updateFn()
			// Forward every progress event to the WebSocket sender
			for pe := range report.Events {
				ep.sendFn(pe.Stage, pe.Status, pe.Message)
			}
			if err := report.Wait(); err != nil {
				slog.Error("Collector update failed", "error", err)
			}
		}()
	default:
		slog.Debug("Received unknown message type from panel", "type", evt.EventType())
	}
}
