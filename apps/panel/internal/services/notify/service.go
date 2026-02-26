package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/services/user"
	sharedtypes "tracer/libs/types"
)

var (
	ErrNotifierNotFound = errors.New("notifier not found")
	ErrAccessDenied     = errors.New("access denied")
	ErrInvalidNotifier  = errors.New("invalid notifier ID")
)

// NotifierAppSubscription represents a subscription to a specific collector-app pair.
type NotifierAppSubscription struct {
	AppName       string
	CollectorName string
}

// NotifierSetup contains notifier configuration with its app subscriptions.
type NotifierSetup struct {
	ID            int64
	UserID        int64
	Name          string
	Type          string
	Config        string
	Enabled       int64
	Subscriptions []NotifierAppSubscription
}

// AddNotifierRequest contains data for creating a new notifier.
// Config is a JSON string specific to the notifier type (e.g., Telegram config).
type AddNotifierRequest struct {
	UserID int64
	Name   string
	Type   string
	Config string // JSON config, type-specific
}

// Service manages notifications, coordinates between components, and handles database operations.
type Service struct {
	db         *sql.DB
	queries    *db.Queries
	userSvc    *user.Service
	registry   *Registry
	dispatcher *Dispatcher
	factories  map[string]FromRawJSON
}

// New creates a new notification service.
func New(sqlDB *sql.DB, userSvc *user.Service, registry *Registry, dispatcher *Dispatcher, factories map[string]FromRawJSON) *Service {
	return &Service{
		db:         sqlDB,
		queries:    db.New(sqlDB),
		userSvc:    userSvc,
		registry:   registry,
		dispatcher: dispatcher,
		factories:  factories,
	}
}

// Chan returns a send-only channel for submitting notifications.
func (s *Service) Chan() chan<- Notification {
	return s.dispatcher.Sink()
}

// AddNotifier creates a notifier, tests it by sending welcome notification, then saves to DB.
// The notifier is only persisted if the welcome notification succeeds.
func (s *Service) AddNotifier(ctx context.Context, req AddNotifierRequest) error {
	// 1. Create notifier instance to test it works
	factory, ok := s.factories[req.Type]
	if !ok {
		return fmt.Errorf("unknown notifier type: %s", req.Type)
	}
	instance, err := factory(req.Name, req.Config)
	if err != nil {
		return fmt.Errorf("could not create notifier instance: %w", err)
	}

	// 2. Send welcome notification - must succeed before saving to DB
	welcomeNotif := Notification{
		Type:    TypeWelcome,
		AppName: "tracer",
		Message: "Cheers, love! The cavalry's here!",
	}
	if err := instance.Notify(welcomeNotif); err != nil {
		return fmt.Errorf("cound not send welcome notification: %w", err)
	}
	slog.Info("Sent welcome notification", "notifier", req.Name)

	// 3. Save to database only after welcome notification succeeded
	_, err = s.queries.CreateNotifier(ctx, db.CreateNotifierParams{
		UserID:  req.UserID,
		Name:    req.Name,
		Type:    req.Type,
		Config:  req.Config,
		Enabled: int64(1),
	})
	if err != nil {
		return fmt.Errorf("create notifier in DB: %w", err)
	}

	slog.Info("Notifier added successfully", "name", req.Name)
	return nil
}

// GetSetup retrieves a notifier by ID along with its app subscriptions.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) GetSetup(ctx context.Context, user sharedtypes.User, notifierID int64) (*NotifierSetup, error) {
	notifier, err := s.queries.GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotifierNotFound
		}
		return nil, err
	}

	if !s.canAccessNotifier(user, notifier.UserID) {
		return nil, ErrAccessDenied
	}

	subscriptions, err := s.getNotifierApps(ctx, notifierID)
	if err != nil {
		return nil, fmt.Errorf("get notifier apps: %w", err)
	}

	return &NotifierSetup{
		ID:            notifier.ID,
		UserID:        notifier.UserID,
		Name:          notifier.Name,
		Type:          notifier.Type,
		Config:        notifier.Config,
		Enabled:       notifier.Enabled,
		Subscriptions: subscriptions,
	}, nil
}

// DeleteNotifier removes a notifier with access check.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) DeleteNotifier(ctx context.Context, user sharedtypes.User, notifierID int64) error {
	// Verify notifier exists and user has access
	notifier, err := s.queries.GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotifierNotFound
		}
		return err
	}

	if !s.canAccessNotifier(user, notifier.UserID) {
		return ErrAccessDenied
	}

	return s.queries.DeleteNotifier(ctx, notifierID)
}

// ToggleNotifier enables or disables a notifier with access check.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) ToggleNotifier(ctx context.Context, user sharedtypes.User, notifierID int64, enable bool) error {
	// Verify notifier exists and user has access
	notifier, err := s.queries.GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotifierNotFound
		}
		return err
	}

	if !s.canAccessNotifier(user, notifier.UserID) {
		return ErrAccessDenied
	}

	if enable {
		return s.queries.EnableNotifier(ctx, notifierID)
	}
	return s.queries.DisableNotifier(ctx, notifierID)
}

// UpdateNotifierApps updates apps for a notifier with access check.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) UpdateNotifierApps(ctx context.Context, user sharedtypes.User, notifierID int64, subscriptions []NotifierAppSubscription) error {
	// 1. Get current setup with access check
	setup, err := s.GetSetup(ctx, user, notifierID)
	if err != nil {
		return err
	}

	// 2. Build map for comparison (key: "collector:app")
	currentSubMap := make(map[string]bool)
	for _, sub := range setup.Subscriptions {
		key := sub.CollectorName + ":" + sub.AppName
		currentSubMap[key] = true
	}

	// 3. Identify newly added subscriptions
	var newlyAdded []NotifierAppSubscription
	for _, sub := range subscriptions {
		key := sub.CollectorName + ":" + sub.AppName
		if !currentSubMap[key] {
			newlyAdded = append(newlyAdded, sub)
		}
	}

	// 4. Update database
	if err := s.SetNotifierApps(ctx, notifierID, subscriptions); err != nil {
		return fmt.Errorf("update notifier subscriptions: %w", err)
	}

	if setup.Enabled != 1 {
		// Notifier disabled, skip notifications and registration
		return nil
	}

	factory, ok := s.factories[setup.Type]
	if !ok {
		return fmt.Errorf("unknown notifier type: %s", setup.Type)
	}
	instance, err := factory(setup.Name, setup.Config)
	if err != nil {
		return fmt.Errorf("create notifier instance: %w", err)
	}

	// 5. Send subscription notifications for newly added subscriptions
	for _, sub := range newlyAdded {
		var message string

		if sub.CollectorName == "*" && sub.AppName == "*" {
			message = "Subscribed to notifications from **all applications** on **all collectors**"
		} else if sub.AppName == "*" {
			message = fmt.Sprintf("Subscribed to notifications from **all applications** on collector **%s**", sub.CollectorName)
		} else if sub.CollectorName == "*" {
			message = fmt.Sprintf("Subscribed to notifications from **%s** on **any collector**", sub.AppName)
		} else {
			message = fmt.Sprintf("Subscribed to notifications from **%s** on collector **%s**", sub.AppName, sub.CollectorName)
		}

		subscriptionNotif := Notification{
			Type:          TypeSubscription,
			CollectorName: sub.CollectorName,
			AppName:       sub.AppName,
			Message:       message,
		}

		if err := instance.Notify(subscriptionNotif); err != nil {
			slog.Error("Failed to send subscription notification", "collector", sub.CollectorName, "app", sub.AppName, "error", err)
		} else {
			slog.Info("Sent subscription notification", "notifier", setup.Name, "collector", sub.CollectorName, "app", sub.AppName)
		}
	}

	// 6. Clear old registrations
	s.registry.ClearByName(setup.Name)

	// 7. Re-register for new subscriptions
	for _, sub := range subscriptions {
		s.registry.Register(sub.CollectorName, sub.AppName, instance)
	}

	return nil
}

// LoadEnabled loads and registers enabled notifiers from the database.
func (s *Service) LoadEnabled(ctx context.Context) error {
	notifiers, err := s.queries.ListAllNotifiers(ctx)
	if err != nil {
		return err
	}

	for _, notifier := range notifiers {
		if notifier.Enabled != 1 {
			continue
		}

		subscriptions, err := s.getNotifierApps(ctx, notifier.ID)
		if err != nil {
			slog.Error("Failed to get notifier subscriptions", "notifier_id", notifier.ID, "error", err)
			continue
		}

		if len(subscriptions) == 0 {
			continue
		}

		owner, err := s.userSvc.GetByID(ctx, notifier.UserID)
		if err != nil {
			slog.Error("Failed to get notifier owner", "notifier_id", notifier.ID, "error", err)
			continue
		}

		if owner.IsAdmin != 1 {
			userApps, err := s.userSvc.GetApps(ctx, owner.ID)
			if err != nil {
				slog.Error("Failed to get user apps", "user_id", owner.ID, "error", err)
				continue
			}
			userAppMap := make(map[string]bool)
			for _, app := range userApps {
				userAppMap[app] = true
			}
			var filtered []NotifierAppSubscription
			for _, sub := range subscriptions {
				if sub.AppName == "*" {
					continue
				}
				if userAppMap[sub.AppName] {
					filtered = append(filtered, sub)
				}
			}
			subscriptions = filtered
		}

		if len(subscriptions) == 0 {
			continue
		}

		factory, ok := s.factories[notifier.Type]
		if !ok {
			slog.Error("Unknown notifier type", "notifier_id", notifier.ID, "type", notifier.Type)
			continue
		}
		instance, err := factory(notifier.Name, notifier.Config)
		if err != nil {
			slog.Error("Failed to create notifier", "notifier_id", notifier.ID, "error", err)
			continue
		}

		for _, sub := range subscriptions {
			s.registry.Register(sub.CollectorName, sub.AppName, instance)
		}

		slog.Info("Loaded notifier", "name", notifier.Name, "type", notifier.Type, "subscriptions", len(subscriptions), "owner", owner.Username)
	}

	return nil
}

// getNotifierApps returns app subscriptions for a notifier (internal use).
func (s *Service) getNotifierApps(ctx context.Context, notifierID int64) ([]NotifierAppSubscription, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT app_name, collector_name FROM notifier_app WHERE notifier_id = ?",
		notifierID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subscriptions []NotifierAppSubscription
	for rows.Next() {
		var sub NotifierAppSubscription
		if err := rows.Scan(&sub.AppName, &sub.CollectorName); err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, sub)
	}

	return subscriptions, rows.Err()
}

// ListNotifiers returns notifiers for the given user.
// Admins see all notifiers, non-admins see only their own.
func (s *Service) ListNotifiers(ctx context.Context, user sharedtypes.User) ([]db.Notifier, error) {
	if user.IsAdmin {
		return s.queries.ListAllNotifiers(ctx)
	}
	return s.queries.ListNotifiersByUser(ctx, user.ID)
}

// SetNotifierApps sets all app subscriptions for a notifier (replaces existing).
func (s *Service) SetNotifierApps(ctx context.Context, notifierID int64, subscriptions []NotifierAppSubscription) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, "DELETE FROM notifier_app WHERE notifier_id = ?", notifierID)
	if err != nil {
		return fmt.Errorf("delete old apps: %w", err)
	}

	for _, sub := range subscriptions {
		if sub.AppName == "" {
			return fmt.Errorf("app_name cannot be empty")
		}
		if sub.CollectorName == "" {
			return fmt.Errorf("collector_name cannot be empty (use '*' for wildcard)")
		}

		_, err = tx.ExecContext(ctx,
			"INSERT INTO notifier_app (notifier_id, app_name, collector_name) VALUES (?, ?, ?)",
			notifierID, sub.AppName, sub.CollectorName)
		if err != nil {
			return fmt.Errorf("insert app: %w", err)
		}
	}

	return tx.Commit()
}

// canAccessNotifier checks if a user can access a notifier based on ownership and admin status.
func (s *Service) canAccessNotifier(user sharedtypes.User, notifierUserID int64) bool {
	if user.IsAdmin {
		return true
	}
	return user.ID == notifierUserID
}
