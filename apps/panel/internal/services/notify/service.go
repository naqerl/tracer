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
	// ErrWelcomeValidation marks a notifier that failed its connection check
	// when being saved (e.g. bad token, unreachable server).
	ErrWelcomeValidation = errors.New("welcome validation failed")
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

// Add creates a notifier for the given actor, tests it by sending welcome
// notification, then saves to DB. The notifier is only persisted if the
// welcome notification succeeds.
func (s *Service) Add(ctx context.Context, actor domain.Actor, req AddNotifierRequest) error {
	if err := s.authorizeAccess(actor, req.UserID); err != nil {
		return err
	}

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
		Payload: "Cheers, love! The cavalry's here!",
	}
	if err := instance.Notify(welcomeNotif); err != nil {
		return fmt.Errorf("could not send welcome notification: %w", err)
	}
	slog.Info("Sent welcome notification", "notifier", req.Name)

	// 3. Save to database only after welcome notification succeeded
	err = s.handle.Write(ctx, func(tx db.DBTX) error {
		_, err := db.New(tx).CreateNotifier(ctx, db.CreateNotifierParams{
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

// SetSubscriptions sets app subscriptions for a notifier.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
func (s *Service) SetSubscriptions(ctx context.Context, actor domain.Actor, notifierID int64, subscriptions []Subscription) error {
	// 1. Verify notifier exists and user has access
	notifier, err := db.New(s.handle.Read()).GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotifierNotFound
		}
		return err
	}

	if err := s.authorizeAccess(actor, notifier.UserID); err != nil {
		return err
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
func (s *Service) GetSetup(ctx context.Context, actor domain.Actor, notifierID int64) (Setup, error) {
	var setup Setup
	notifier, err := db.New(s.handle.Read()).GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return setup, ErrNotifierNotFound
		}
		return setup, err
	}

	if err := s.authorizeAccess(actor, notifier.UserID); err != nil {
		return setup, err
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
func (s *Service) GetSubscriptions(ctx context.Context, actor domain.Actor, notifierID int64) ([]Subscription, error) {
	queries := db.New(s.handle.Read())
	notifier, err := queries.GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotifierNotFound
		}
		return nil, err
	}

	if err := s.authorizeAccess(actor, notifier.UserID); err != nil {
		return nil, err
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

// Delete removes a notifier.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
func (s *Service) Delete(ctx context.Context, actor domain.Actor, notifierID int64) error {
	// Verify notifier exists and user has access
	notifier, err := db.New(s.handle.Read()).GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotifierNotFound
		}
		return err
	}

	if err := s.authorizeAccess(actor, notifier.UserID); err != nil {
		return err
	}

	return s.handle.Write(ctx, func(tx db.DBTX) error {
		return db.New(tx).DeleteNotifier(ctx, notifierID)
	})
}

// Toggle enables or disables a notifier.
// Returns ErrNotifierNotFound if the notifier doesn't exist.
func (s *Service) Toggle(ctx context.Context, actor domain.Actor, notifierID int64, enable bool) error {
	// Verify notifier exists and user has access
	notifier, err := db.New(s.handle.Read()).GetNotifier(ctx, notifierID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotifierNotFound
		}
		return err
	}

	if err := s.authorizeAccess(actor, notifier.UserID); err != nil {
		return err
	}

	return s.handle.Write(ctx, func(tx db.DBTX) error {
		if enable {
			return db.New(tx).EnableNotifier(ctx, notifierID)
		}
		return db.New(tx).DisableNotifier(ctx, notifierID)
	})
}

// List returns notifiers for the given actor.
// Admins see all notifiers, non-admins see only their own.
func (s *Service) List(ctx context.Context, actor domain.Actor) ([]db.Notifier, error) {
	queries := db.New(s.handle.Read())
	switch a := actor.(type) {
	case domain.System:
		return queries.ListAllNotifiers(ctx)
	case domain.User:
		if a.Role == domain.RoleAdmin {
			return queries.ListAllNotifiers(ctx)
		}
		return queries.ListNotifiersByUser(ctx, a.ID)
	case domain.Unauthenticated:
		return nil, ErrAccessDenied
	default:
		return nil, domain.ErrUnexpectedActor
	}
}

// Private methods

// authorizeAccess permits the system actor, admin users, and the user that
// owns the notifier (ownerID).
func (s *Service) authorizeAccess(actor domain.Actor, ownerID int64) error {
	switch a := actor.(type) {
	case domain.System:
		return nil
	case domain.User:
		if a.Role == domain.RoleAdmin || a.ID == ownerID {
			return nil
		}
		return ErrAccessDenied
	case domain.Unauthenticated:
		return ErrAccessDenied
	default:
		return domain.ErrUnexpectedActor
	}
}

// setSubscriptions sets all subscriptions for a notifier (replaces existing).
func (s *Service) setSubscriptions(ctx context.Context, notifierID int64, subscriptions []Subscription) error {
	return s.handle.Write(ctx, func(tx db.DBTX) error {
		queries := db.New(tx)

		if err := queries.RemoveAllNotifierSubscriptions(ctx, notifierID); err != nil {
			return fmt.Errorf("delete old apps: %w", err)
		}

		for _, sub := range subscriptions {
			if sub.AppName == "" {
				return fmt.Errorf("app_name cannot be empty")
			}
			if sub.CollectorName == "" {
				return fmt.Errorf("collector_name cannot be empty (use '*' for wildcard)")
			}

			if err := queries.AddNotifierSubscription(ctx, db.AddNotifierSubscriptionParams{
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
