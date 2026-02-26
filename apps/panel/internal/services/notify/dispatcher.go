package notify

import (
	"context"
	"fmt"
	"log/slog"

	"tracer/apps/panel/db"
)

const maxWorkers = 5

// Dispatcher manages the notification worker pool and dispatches
// notifications to registered notifiers.
type Dispatcher struct {
	ch        chan Notification
	semaphore chan struct{}
	db        db.DBTX
	factories map[string]FromRawJSON
}

// NewDispatcher creates a new notification dispatcher.
func NewDispatcher(db db.DBTX, factories map[string]FromRawJSON, ch chan Notification) *Dispatcher {
	return &Dispatcher{
		ch:        ch,
		semaphore: make(chan struct{}, maxWorkers),
		db:        db,
		factories: factories,
	}
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
			notifiers, err := d.getNotifiers(ctx, notification.CollectorName, notification.AppName)
			if err != nil {
				slog.Error("Failed to get notifiers for notification", "error", err)
				continue
			}
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

// getNotifiers queries the database for matching notifiers and creates instances.
func (d *Dispatcher) getNotifiers(ctx context.Context, collectorName, appName string) ([]Notifier, error) {
	queries := db.New(d.db)
	rows, err := queries.GetMatchingNotifiers(ctx, db.GetMatchingNotifiersParams{
		CollectorName: collectorName,
		AppName:       appName,
	})
	if err != nil {
		return nil, fmt.Errorf("query matching notifiers: %w", err)
	}

	var notifiers []Notifier
	for _, row := range rows {
		factory, ok := d.factories[row.Type]
		if !ok {
			slog.Error("Unknown notifier type", "type", row.Type, "name", row.Name)
			continue
		}
		instance, err := factory(row.Name, row.Config)
		if err != nil {
			slog.Error("Failed to create notifier instance", "name", row.Name, "type", row.Type, "error", err)
			continue
		}
		notifiers = append(notifiers, instance)
	}

	return notifiers, nil
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
