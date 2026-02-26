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

// DB is the database interface for the notify service.
// It includes BeginTx for transaction support.
type DB interface {
	db.DBTX
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

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

// Service manages notifications and handles database operations.
type Service struct {
	db        DB
	userSvc   *user.Service
	factories map[string]FromRawJSON
}

// New creates a new notification service.
func New(sqlDB DB, userSvc *user.Service, factories map[string]FromRawJSON) *Service {
	return &Service{
		db:        sqlDB,
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
		AppName: "tracer",
		Message: "Cheers, love! The cavalry's here!",
	}
	if err := instance.Notify(welcomeNotif); err != nil {
		return fmt.Errorf("cound not send welcome notification: %w", err)
	}
	slog.Info("Sent welcome notification", "notifier", req.Name)

	// 3. Save to database only after welcome notification succeeded
	queries := db.New(s.db)
	_, err = queries.CreateNotifier(ctx, db.CreateNotifierParams{
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
	queries := db.New(s.db)
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

	subscriptions, err := s.getApps(ctx, notifierID)
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

// Delete removes a notifier with access check.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) Delete(ctx context.Context, user sharedtypes.User, notifierID int64) error {
	// Verify notifier exists and user has access
	queries := db.New(s.db)
	notifier, err := queries.GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotifierNotFound
		}
		return err
	}

	if !s.canAccess(user, notifier.UserID) {
		return ErrAccessDenied
	}

	return queries.DeleteNotifier(ctx, notifierID)
}

// Toggle enables or disables a notifier with access check.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) Toggle(ctx context.Context, user sharedtypes.User, notifierID int64, enable bool) error {
	// Verify notifier exists and user has access
	queries := db.New(s.db)
	notifier, err := queries.GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotifierNotFound
		}
		return err
	}

	if !s.canAccess(user, notifier.UserID) {
		return ErrAccessDenied
	}

	if enable {
		return queries.EnableNotifier(ctx, notifierID)
	}
	return queries.DisableNotifier(ctx, notifierID)
}

// SetApps sets app subscriptions for a notifier with access check.
// Simply writes to the database without any registration/caching logic.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
// Returns ErrAccessDenied if the user doesn't have access to the notifier.
func (s *Service) SetApps(ctx context.Context, user sharedtypes.User, notifierID int64, subscriptions []NotifierAppSubscription) error {
	// 1. Verify notifier exists and user has access
	queries := db.New(s.db)
	notifier, err := queries.GetNotifier(ctx, notifierID)
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
	if err := s.setApps(ctx, notifierID, subscriptions); err != nil {
		return fmt.Errorf("update notifier subscriptions: %w", err)
	}

	slog.Info("Notifier subscriptions updated", "notifier_id", notifierID, "subscriptions", len(subscriptions))
	return nil
}

// List returns notifiers for the given user.
// Admins see all notifiers, non-admins see only their own.
func (s *Service) List(ctx context.Context, user sharedtypes.User) ([]db.Notifier, error) {
	queries := db.New(s.db)
	if user.IsAdmin {
		return queries.ListAllNotifiers(ctx)
	}
	return queries.ListNotifiersByUser(ctx, user.ID)
}

// Private methods

// getApps returns app subscriptions for a notifier.
func (s *Service) getApps(ctx context.Context, notifierID int64) ([]NotifierAppSubscription, error) {
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

// setApps sets all app subscriptions for a notifier (replaces existing).
func (s *Service) setApps(ctx context.Context, notifierID int64, subscriptions []NotifierAppSubscription) error {
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

// canAccess checks if a user can access a notifier based on ownership and admin status.
func (s *Service) canAccess(user sharedtypes.User, notifierUserID int64) bool {
	if user.IsAdmin {
		return true
	}
	return user.ID == notifierUserID
}
