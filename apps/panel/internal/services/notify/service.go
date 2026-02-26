package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/services/user"
	sharedtypes "tracer/libs/types"
)

// TelegramConfig represents Telegram notifier configuration.
type TelegramConfig struct {
	Token  string `json:"token"`
	ChatID int64  `json:"chat_id"`
}

// NotifierAppSubscription represents a subscription to a specific collector-app pair.
type NotifierAppSubscription struct {
	AppName       string
	CollectorName string
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
}

// New creates a new notification service.
func New(sqlDB *sql.DB, userSvc *user.Service, registry *Registry, dispatcher *Dispatcher) *Service {
	return &Service{
		db:         sqlDB,
		queries:    db.New(sqlDB),
		userSvc:    userSvc,
		registry:   registry,
		dispatcher: dispatcher,
	}
}

// Chan returns a send-only channel for submitting notifications.
func (s *Service) Chan() chan<- Notification {
	return s.dispatcher.Sink()
}

// createNotifier creates a notifier instance from configuration.
// Supported types:
//   - "telegram": requires TelegramConfig JSON with "token" and "chat_id"
func (s *Service) createNotifier(name, ntype, config string) (Notifier, error) {
	switch ntype {
	case "telegram":
		var cfg TelegramConfig
		if err := json.Unmarshal([]byte(config), &cfg); err != nil {
			return nil, fmt.Errorf("parse telegram config: %w", err)
		}
		return NewTelegramNotifier(name, cfg.Token, cfg.ChatID)
	default:
		return nil, fmt.Errorf("unknown notifier type: %s", ntype)
	}
}

// AddNotifier creates a notifier, tests it by sending welcome notification, then saves to DB.
// The notifier is only persisted if the welcome notification succeeds.
func (s *Service) AddNotifier(ctx context.Context, req AddNotifierRequest) error {
	// 1. Create notifier instance to test it works
	instance, err := s.createNotifier(req.Name, req.Type, req.Config)
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

// UpdateNotifierApps updates apps for a notifier, sends subscription notifications for new apps.
func (s *Service) UpdateNotifierApps(ctx context.Context, notifierID int64, userID int64, newSubscriptions []NotifierAppSubscription) error {
	// 1. Get current subscriptions
	currentSubscriptions, err := s.GetNotifierApps(ctx, notifierID)
	if err != nil {
		return fmt.Errorf("get current subscriptions: %w", err)
	}

	// 2. Build map for comparison (key: "collector:app")
	currentSubMap := make(map[string]bool)
	for _, sub := range currentSubscriptions {
		key := sub.CollectorName + ":" + sub.AppName
		currentSubMap[key] = true
	}

	// 3. Identify newly added subscriptions
	var newlyAdded []NotifierAppSubscription
	for _, sub := range newSubscriptions {
		key := sub.CollectorName + ":" + sub.AppName
		if !currentSubMap[key] {
			newlyAdded = append(newlyAdded, sub)
		}
	}

	// 4. Update database
	if err := s.SetNotifierApps(ctx, notifierID, newSubscriptions); err != nil {
		return fmt.Errorf("update notifier subscriptions: %w", err)
	}

	// 5. Get notifier and create instance
	notifier, err := s.GetNotifierDB(ctx, notifierID)
	if err != nil {
		return fmt.Errorf("get notifier: %w", err)
	}

	if notifier.Enabled != 1 {
		// Notifier disabled, skip notifications and registration
		return nil
	}

	instance, err := s.createNotifier(notifier.Name, notifier.Type, notifier.Config)
	if err != nil {
		return fmt.Errorf("create notifier instance: %w", err)
	}

	// 6. Send subscription notifications for newly added subscriptions
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
			slog.Info("Sent subscription notification", "notifier", notifier.Name, "collector", sub.CollectorName, "app", sub.AppName)
		}
	}

	// 7. Clear old registrations
	s.registry.ClearByName(notifier.Name)

	// 8. Re-register for new subscriptions
	for _, sub := range newSubscriptions {
		s.registry.Register(sub.CollectorName, sub.AppName, instance)
	}

	return nil
}

// LoadEnabled loads and registers enabled notifiers from the database.
func (s *Service) LoadEnabled(ctx context.Context) error {
	notifiers, err := s.ListNotifiers(ctx)
	if err != nil {
		return err
	}

	for _, notifier := range notifiers {
		if notifier.Enabled != 1 {
			continue
		}

		subscriptions, err := s.GetNotifierApps(ctx, notifier.ID)
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

		instance, err := s.createNotifier(notifier.Name, notifier.Type, notifier.Config)
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

// Database methods (using sqlc querier).

// GetNotifierDB retrieves a notifier by ID (returns db.Notifier).
func (s *Service) GetNotifierDB(ctx context.Context, id int64) (*db.Notifier, error) {
	notifier, err := s.queries.GetNotifier(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("notifier not found")
		}
		return nil, err
	}
	return &notifier, nil
}

// GetNotifierByName retrieves a notifier by name.
func (s *Service) GetNotifierByName(ctx context.Context, name string) (*db.Notifier, error) {
	notifier, err := s.queries.GetNotifierByName(ctx, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("notifier not found")
		}
		return nil, err
	}
	return &notifier, nil
}

// ListNotifiers returns all notifiers.
func (s *Service) ListNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.ListNotifiers(ctx)
}

// ListNotifiersByUser returns notifiers for a specific user.
func (s *Service) ListNotifiersByUser(ctx context.Context, userID int64) ([]db.Notifier, error) {
	return s.queries.ListNotifiersByUser(ctx, userID)
}

// ListEnabledNotifiers returns all enabled notifiers.
func (s *Service) ListEnabledNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.ListEnabledNotifiers(ctx)
}

// UpdateNotifier updates a notifier's details.
func (s *Service) UpdateNotifier(ctx context.Context, id int64, name, ntype, config string, enabled bool) error {
	enabledInt := int64(0)
	if enabled {
		enabledInt = 1
	}
	return s.queries.UpdateNotifier(ctx, db.UpdateNotifierParams{
		ID:      id,
		Name:    name,
		Type:    ntype,
		Config:  config,
		Enabled: enabledInt,
	})
}

// DeleteNotifier removes a notifier.
func (s *Service) DeleteNotifier(ctx context.Context, id int64) error {
	return s.queries.DeleteNotifier(ctx, id)
}

// EnableNotifier enables a notifier.
func (s *Service) EnableNotifier(ctx context.Context, id int64) error {
	return s.queries.EnableNotifier(ctx, id)
}

// DisableNotifier disables a notifier.
func (s *Service) DisableNotifier(ctx context.Context, id int64) error {
	return s.queries.DisableNotifier(ctx, id)
}

// GetNotifierApps returns app subscriptions for a notifier.
func (s *Service) GetNotifierApps(ctx context.Context, notifierID int64) ([]NotifierAppSubscription, error) {
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

// GetNotifiersForApp returns notifiers subscribed to a specific app.
func (s *Service) GetNotifiersForApp(ctx context.Context, appName string) ([]db.Notifier, error) {
	return s.queries.GetNotifiersForApp(ctx, appName)
}

// GetGlobalNotifiers returns notifiers subscribed to all apps.
func (s *Service) GetGlobalNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.GetGlobalNotifiers(ctx)
}

// Permission check helpers.

// CanUserAccessNotifier checks if a user can access a notifier based on ownership and admin status.
func (s *Service) CanUserAccessNotifier(ctx context.Context, user sharedtypes.User, notifierUserID int64) bool {
	if user.IsAdmin {
		return true
	}
	return user.ID == notifierUserID
}

// CanUserAccessApp checks if a user has access to a specific app.
func (s *Service) CanUserAccessApp(ctx context.Context, userID int64, appName string) (bool, error) {
	return s.userSvc.HasAppAccess(ctx, userID, appName)
}
