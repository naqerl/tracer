package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/user"
)

const maxWorkers = 5

// Dispatcher manages the notification worker pool and dispatches
// notifications to registered notifiers.
type Dispatcher struct {
	ch        chan Notification
	semaphore chan struct{}
	handle    *dbmanager.DBHandle
	factories map[string]FromRawJSON
	userSvc   *user.Service
}

// NewDispatcher creates a new notification dispatcher.
func NewDispatcher(handle *dbmanager.DBHandle, factories map[string]FromRawJSON, ch chan Notification, userSvc *user.Service) *Dispatcher {
	return &Dispatcher{
		ch:        ch,
		semaphore: make(chan struct{}, maxWorkers),
		handle:    handle,
		factories: factories,
		userSvc:   userSvc,
	}
}

// Start begins processing notifications. Blocks until context is cancelled.
func (d *Dispatcher) Start(ctx context.Context) {
	slog.Info("Notification dispatcher started")
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Notification dispatcher stopping")
			return
		case notification := <-d.ch:
			notifiers, err := d.getNotifiers(ctx, notification.CollectorName, notification.AppName)
			if err != nil {
				slog.Error("Failed to get notifiers for notification", "error", err)
				// NOTE: Absence of continue is intentional.
				// Even with non-nil error there coul be some notifier
			}
			if len(notifiers) == 0 {
				continue
			}

			select {
			case d.semaphore <- struct{}{}:
			case <-ctx.Done():
				slog.Info("Notification dispatcher stopping")
				return
			}

			wg.Go(func() {
				defer func() { <-d.semaphore }()
				d.dispatch(notification, notifiers)
			})
		}
	}
}

// getNotifiers queries the database for matching notifiers and creates instances.
func (d *Dispatcher) getNotifiers(ctx context.Context, collectorName, appName string) ([]Notifier, error) {
	queries := db.New(d.handle.Read())
	rows, err := queries.GetMatchingNotifiers(ctx, db.GetMatchingNotifiersParams{
		CollectorName: collectorName,
		AppName:       appName,
	})
	if err != nil {
		return nil, fmt.Errorf("could not get notifiers: %w", err)
	}

	var notifiers []Notifier
	var errs []error
	for _, row := range rows {
		access, err := d.userSvc.FindAccessPolicy(ctx, row.UserID)
		if err != nil {
			errs = append(errs, fmt.Errorf("could not list owner's apps for notifier %d: %w", row.ID, err))
			continue
		}

		allowed := access.AllowedTo(collectorName, appName)
		if !allowed {
			continue
		}

		factory, ok := d.factories[row.Type]
		if !ok {
			errs = append(errs, fmt.Errorf("factory not found for notifier %d", row.ID))
			continue
		}

		instance, err := factory(row.Name, row.Config)
		if err != nil {
			errs = append(errs, fmt.Errorf("could not create notifier %d: %w", row.ID, err))
			continue
		}

		notifiers = append(notifiers, instance)
	}

	if len(errs) > 0 {
		err = fmt.Errorf("could not setup one or more notifiers for %s:%s with %w", collectorName, appName, errors.Join(errs...))
	}

	return notifiers, err
}

// dispatch sends a notification to all provided notifiers. Notifications
// carry structured data; each notifier renders its own presentation.
func (d *Dispatcher) dispatch(n Notification, notifiers []Notifier) {
	for _, notifier := range notifiers {
		if err := notifier.Notify(n); err != nil {
			slog.Error("Failed to send notification",
				"notifier_type", notifier.Type(),
				"notifier_name", notifier.Name(),
				"app", n.AppName,
				"error", err,
			)
		}
	}
}
