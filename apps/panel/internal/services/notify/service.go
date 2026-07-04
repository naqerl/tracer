package notify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/services/user"
)

var (
	ErrNotifierNotFound = errors.New("notifier not found")
	ErrAccessDenied     = errors.New("access denied")
	ErrInvalidNotifier  = errors.New("invalid notifier ID")
)

// Subscription represents a subscription to a specific collector-app pair.
type Subscription struct {
	AppName       string
	CollectorName string
}

// Setup contains notifier configuration without subscriptions.
type Setup struct {
	ID      int64
	UserID  int64
	Name    string
	Type    string
	Config  string
	Enabled int64
}

// AddNotifierRequest contains data for creating a new notifier.
// Config is a JSON string specific to the notifier type (e.g., Telegram config).
type AddNotifierRequest struct {
	UserID int64
	Name   string
	Type   string
	Config string // JSON config, type-specific
}

func (r AddNotifierRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if err := domain.ValidateNotifierName(r.Name); err != nil {
		return err
	}
	if r.Type == "" {
		return fmt.Errorf("type is required")
	}
	if r.Config == "" {
		return fmt.Errorf("config is required")
	}
	return nil
}

// Service manages notifications and handles database operations.
type Service struct {
	handle    *dbmanager.DBHandle
	userSvc   *user.Service
	factories map[string]FromRawJSON
}

// New creates a new notification service.
func New(handle *dbmanager.DBHandle, userSvc *user.Service, factories map[string]FromRawJSON) *Service {
	return &Service{
		handle:    handle,
		userSvc:   userSvc,
		factories: factories,
	}
}

// Add creates a notifier, tests it by sending welcome notification, then saves to DB.
// The notifier is only persisted if the welcome notification succeeds.
func (s *Service) Add(ctx context.Context, req AddNotifierRequest) error {
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
		Message: "Cheers, love! The cavalry's here!",
	}
	if err := instance.Notify(welcomeNotif); err != nil {
		return fmt.Errorf("cound not send welcome notification: %w", err)
	}
	slog.Info("Sent welcome notification", "notifier", req.Name)

	// 3. Save to database only after welcome notification succeeded
	err = s.handle.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		_, err := db.New(tx).CreateNotifier(wctx, db.CreateNotifierParams{
			UserID:    req.UserID,
			Name:      req.Name,
			Type:      req.Type,
			Config:    req.Config,
			Enabled:   int64(1),
			CreatedAt: time.Now().UTC().Unix(),
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("create notifier in DB: %w", err)
	}

	slog.Info("Notifier added successfully", "name", req.Name)
	return nil
}

// SetSubscriptions sets app subscriptions for a notifier with access check.
// Simply writes to the database without any registration/caching logic.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) SetSubscriptions(ctx context.Context, user user.User, notifierID int64, subscriptions []Subscription) error {
	// 1. Verify notifier exists and user has access
	notifier, err := db.New(s.handle.Read(ctx)).GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotifierNotFound
		}
		return err
	}

	if !s.canAccess(user, notifier.UserID) {
		return ErrAccessDenied
	}

	// 2. Simply update database - no registration/caching logic
	if err := s.setSubscriptions(ctx, notifierID, subscriptions); err != nil {
		return fmt.Errorf("update notifier subscriptions: %w", err)
	}

	slog.Info("Notifier subscriptions updated", "notifier_id", notifierID, "subscriptions", len(subscriptions))
	return nil
}

// GetSetup retrieves a notifier setup by ID (without subscriptions).
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) GetSetup(ctx context.Context, user user.User, notifierID int64) (Setup, error) {
	var setup Setup
	notifier, err := db.New(s.handle.Read(ctx)).GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return setup, ErrNotifierNotFound
		}
		return setup, err
	}

	if !s.canAccess(user, notifier.UserID) {
		return setup, ErrAccessDenied
	}

	return Setup{
		ID:      notifier.ID,
		UserID:  notifier.UserID,
		Name:    notifier.Name,
		Type:    notifier.Type,
		Config:  notifier.Config,
		Enabled: notifier.Enabled,
	}, nil
}

// GetSubscriptions retrieves subscriptions for a notifier by ID.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) GetSubscriptions(ctx context.Context, user user.User, notifierID int64) ([]Subscription, error) {
	queries := db.New(s.handle.Read(ctx))
	notifier, err := queries.GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotifierNotFound
		}
		return nil, err
	}

	if !s.canAccess(user, notifier.UserID) {
		return nil, ErrAccessDenied
	}

	rows, err := queries.GetNotifierSubscriptions(ctx, notifierID)
	if err != nil {
		return nil, fmt.Errorf("get notifier subscriptions: %w", err)
	}

	var subscriptions []Subscription
	for _, row := range rows {
		subscriptions = append(subscriptions, Subscription{
			AppName:       row.AppName,
			CollectorName: row.CollectorName,
		})
	}

	return subscriptions, nil
}

// Delete removes a notifier with access check.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) Delete(ctx context.Context, user user.User, notifierID int64) error {
	// Verify notifier exists and user has access
	notifier, err := db.New(s.handle.Read(ctx)).GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotifierNotFound
		}
		return err
	}

	if !s.canAccess(user, notifier.UserID) {
		return ErrAccessDenied
	}

	return s.handle.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		return db.New(tx).DeleteNotifier(wctx, notifierID)
	})
}

// Toggle enables or disables a notifier with access check.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) Toggle(ctx context.Context, user user.User, notifierID int64, enable bool) error {
	// Verify notifier exists and user has access
	notifier, err := db.New(s.handle.Read(ctx)).GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotifierNotFound
		}
		return err
	}

	if !s.canAccess(user, notifier.UserID) {
		return ErrAccessDenied
	}

	return s.handle.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		if enable {
			return db.New(tx).EnableNotifier(wctx, notifierID)
		}
		return db.New(tx).DisableNotifier(wctx, notifierID)
	})
}

// List returns notifiers for the given user.
// Admins see all notifiers, non-admins see only their own.
func (s *Service) List(ctx context.Context, user user.User) ([]db.Notifier, error) {
	queries := db.New(s.handle.Read(ctx))
	if user.IsAdmin {
		return queries.ListAllNotifiers(ctx)
	}
	return queries.ListNotifiersByUser(ctx, user.ID)
}

// Private methods

// setSubscriptions sets all subscriptions for a notifier (replaces existing).
func (s *Service) setSubscriptions(ctx context.Context, notifierID int64, subscriptions []Subscription) error {
	return s.handle.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		queries := db.New(tx)

		if err := queries.RemoveAllNotifierSubscriptions(wctx, notifierID); err != nil {
			return fmt.Errorf("delete old apps: %w", err)
		}

		for _, sub := range subscriptions {
			if sub.AppName == "" {
				return fmt.Errorf("app_name cannot be empty")
			}
			if sub.CollectorName == "" {
				return fmt.Errorf("collector_name cannot be empty (use '*' for wildcard)")
			}

			if err := queries.AddNotifierSubscription(wctx, db.AddNotifierSubscriptionParams{
				NotifierID:    notifierID,
				AppName:       sub.AppName,
				CollectorName: sub.CollectorName,
			}); err != nil {
				return fmt.Errorf("insert app: %w", err)
			}
		}

		return nil
	})
}

// canAccess checks if a user can access a notifier based on ownership and admin status.
func (s *Service) canAccess(user user.User, notifierUserID int64) bool {
	if user.IsAdmin {
		return true
	}
	return user.ID == notifierUserID
}
