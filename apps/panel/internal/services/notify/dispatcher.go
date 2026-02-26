package notify

import (
	"context"
	"fmt"
	"log/slog"
)

const (
	maxWorkers  = 5
	channelSize = 100
)

// Dispatcher manages the notification worker pool and dispatches
// notifications to registered notifiers.
type Dispatcher struct {
	ch        chan Notification
	semaphore chan struct{}
	registry  *Registry
}

// NewDispatcher creates a new notification dispatcher.
func NewDispatcher(registry *Registry) *Dispatcher {
	return &Dispatcher{
		ch:        make(chan Notification, channelSize),
		semaphore: make(chan struct{}, maxWorkers),
		registry:  registry,
	}
}

// Sink returns a send-only channel for submitting notifications.
func (d *Dispatcher) Sink() chan<- Notification {
	return d.ch
}

// Start begins processing notifications. Blocks until context is cancelled.
func (d *Dispatcher) Start(ctx context.Context) {
	slog.Info("Notification dispatcher started")

	for {
		select {
		case <-ctx.Done():
			slog.Info("Notification dispatcher stopping")
			return
		case notification := <-d.ch:
			notifiers := d.registry.GetNotifiers(notification.CollectorName, notification.AppName)
			if len(notifiers) == 0 {
				continue
			}

			d.semaphore <- struct{}{}
			go func(n Notification, notifiers []Notifier) {
				defer func() { <-d.semaphore }()
				d.dispatch(n, notifiers)
			}(notification, notifiers)
		}
	}
}

// dispatch formats and sends a notification to all provided notifiers.
func (d *Dispatcher) dispatch(n Notification, notifiers []Notifier) {
	// Map notification type to emoji prefix
	var prefix string
	switch n.Type {
	case TypeAlert:
		prefix = "⚠️ alert"
	case TypeLifecycle:
		prefix = "lifecycle"
	case TypeWelcome:
		prefix = "👋 welcome"
	case TypeSubscription:
		prefix = "🔔 subscription"
	default:
		prefix = string(n.Type)
	}

	// Build final formatted message: {prefix} @{app}\n{message}
	formattedMessage := fmt.Sprintf("%s @%s\n%s", prefix, n.AppName, n.Message)

	// Create notification with formatted message for sending
	finalNotification := Notification{
		Type:          n.Type,
		CollectorName: n.CollectorName,
		AppName:       n.AppName,
		Message:       formattedMessage,
	}

	for _, notifier := range notifiers {
		if err := notifier.Notify(finalNotification); err != nil {
			slog.Error("Failed to send notification",
				"notifier_type", notifier.Type(),
				"notifier_name", notifier.Name(),
				"app", n.AppName,
				"error", err,
			)
		}
	}
}
