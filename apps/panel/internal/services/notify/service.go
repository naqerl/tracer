package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/services/user"
	sharedtypes "github.com/ai-shift/tracer/libs/types"
)

const (
	maxWorkers  = 5
	channelSize = 100
	allAppsKey  = "*" // Special key meaning "all apps"
)

// CollectorApp represents a collector-app pair for subscription routing
type CollectorApp struct {
	CollectorName string
	AppName       string
}

// Service manages notifications, dispatches them to appropriate notifiers, and handles database operations
type Service struct {
	ch        chan Notification
	semaphore chan struct{}

	mu            sync.RWMutex
	subscriptions map[CollectorApp][]Notifier // (collector, app) -> notifiers

	db          *sql.DB
	queries     *db.Queries
	userService *user.Service
}

// AddNotifierRequest contains data for creating a new notifier
type AddNotifierRequest struct {
	UserID      int64
	Name        string
	Type        string
	Token       string // For Telegram
	ChatID      int64  // For Telegram
	InitialApps []string
}

// NotifierAppSubscription represents a subscription to a specific collector-app pair
type NotifierAppSubscription struct {
	AppName       string
	CollectorName string
}

// NotifierData represents a notifier from the database
type NotifierData struct {
	ID      int64
	UserID  int64
	Name    string
	Type    string
	Config  string
	Enabled bool
}

// TelegramConfig represents Telegram notifier configuration
type TelegramConfig struct {
	Token  string `json:"token"`
	ChatID int64  `json:"chat_id"`
}

// New creates a new notification service
func New(sqlDB *sql.DB, userService *user.Service) *Service {
	return &Service{
		ch:            make(chan Notification, channelSize),
		semaphore:     make(chan struct{}, maxWorkers),
		subscriptions: make(map[CollectorApp][]Notifier),
		db:            sqlDB,
		queries:       db.New(sqlDB),
		userService:   userService,
	}
}

// Chan returns a send-only channel for submitting notifications
func (s *Service) Chan() NotificationChan {
	return s.ch
}

// RegisterNotifier registers a notifier for specific collector-app combination
// Use "*" as appName to register for all apps from a collector
// Use "*" as collectorName to register for an app from all collectors
// Use "*" for both to register for all apps from all collectors
func (s *Service) RegisterNotifier(collectorName, appName string, notifier Notifier) {
	if collectorName == "" || appName == "" {
		slog.Error("Cannot register notifier with empty collector or app name", "collector", collectorName, "app", appName, "notifier", notifier.Name())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := CollectorApp{CollectorName: collectorName, AppName: appName}
	s.subscriptions[key] = append(s.subscriptions[key], notifier)
	slog.Info("Notifier registered", "type", notifier.Type(), "name", notifier.Name(), "collector", collectorName, "app", appName)
}

// UnregisterNotifier removes a notifier by name for a collector-app pair
func (s *Service) UnregisterNotifier(collectorName, appName, notifierName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := CollectorApp{CollectorName: collectorName, AppName: appName}
	notifiers := s.subscriptions[key]
	for i, n := range notifiers {
		if n.Name() == notifierName {
			s.subscriptions[key] = append(notifiers[:i], notifiers[i+1:]...)
			slog.Info("Notifier unregistered", "name", notifierName, "collector", collectorName, "app", appName)
			return
		}
	}
}

// ClearNotifiers removes all notifiers for a collector-app pair
func (s *Service) ClearNotifiers(collectorName, appName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subscriptions, CollectorApp{CollectorName: collectorName, AppName: appName})
}

// getNotifiersForApp returns all notifiers that should receive notifications for a collector-app pair
func (s *Service) getNotifiersForApp(collectorName, appName string) []Notifier {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Notifier

	// 1. Exact match: collector + app
	key := CollectorApp{CollectorName: collectorName, AppName: appName}
	if notifiers, ok := s.subscriptions[key]; ok {
		result = append(result, notifiers...)
	}

	// 2. Any collector for this app (collector = "*")
	keyAnyCollector := CollectorApp{CollectorName: allAppsKey, AppName: appName}
	if notifiers, ok := s.subscriptions[keyAnyCollector]; ok {
		result = append(result, notifiers...)
	}

	// 3. All apps from this specific collector (app = "*", collector = specific)
	keyCollectorWildcard := CollectorApp{CollectorName: collectorName, AppName: allAppsKey}
	if notifiers, ok := s.subscriptions[keyCollectorWildcard]; ok {
		result = append(result, notifiers...)
	}

	// 4. All apps from any collector (app = "*", collector = "*")
	keyGlobalWildcard := CollectorApp{CollectorName: allAppsKey, AppName: allAppsKey}
	if notifiers, ok := s.subscriptions[keyGlobalWildcard]; ok {
		result = append(result, notifiers...)
	}

	return result
}

// Start begins processing notifications
func (s *Service) Start(ctx context.Context) {
	slog.Info("Notification service started")

	for {
		select {
		case <-ctx.Done():
			slog.Info("Notification service stopping")
			return
		case notification := <-s.ch:
			notifiers := s.getNotifiersForApp(notification.CollectorName, notification.AppName)
			if len(notifiers) == 0 {
				continue
			}

			s.semaphore <- struct{}{}
			go func(n Notification, notifiers []Notifier) {
				defer func() { <-s.semaphore }()
				s.dispatch(n, notifiers)
			}(notification, notifiers)
		}
	}
}

func (s *Service) dispatch(n Notification, notifiers []Notifier) {
	// Map notification type to emoji prefix
	var prefix string
	switch n.Type {
	case TypeAlert:
		prefix = "⚠️ alert"
	case TypeStartup:
		prefix = "🚀 startup"
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

// AddNotifier creates a notifier, saves to DB, sends welcome notification, and registers it
// This is the entry point for creating new notifiers from the UI
func (s *Service) AddNotifier(ctx context.Context, req AddNotifierRequest) error {
	// 1. Create config JSON based on type
	var configJSON string
	switch req.Type {
	case "telegram":
		cfg := struct {
			Token  string `json:"token"`
			ChatID int64  `json:"chat_id"`
		}{
			Token:  req.Token,
			ChatID: req.ChatID,
		}
		cfgBytes, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}
		configJSON = string(cfgBytes)
	default:
		return fmt.Errorf("unsupported notifier type: %s", req.Type)
	}

	// 2. Save to database
	notifier, err := s.CreateNotifier(ctx, req.UserID, req.Name, req.Type, configJSON, true)
	if err != nil {
		return fmt.Errorf("create notifier in DB: %w", err)
	}

	// 3. Create notifier instance
	instance, err := s.createNotifierInstance(notifier)
	if err != nil {
		return fmt.Errorf("create notifier instance: %w", err)
	}

	// 4. Send welcome notification
	welcomeNotif := Notification{
		Type:          TypeWelcome,
		CollectorName: "",
		AppName:       "tracer",
		Message:       "Cheers, love! The cavalry's here!",
	}

	if err := instance.Notify(welcomeNotif); err != nil {
		slog.Error("Failed to send welcome notification", "error", err)
		// Don't fail the whole operation if welcome message fails
	} else {
		slog.Info("Sent welcome notification", "notifier", req.Name)
	}

	// 5. Register notifier for apps (if any)
	// InitialApps is usually empty - users configure subscriptions later via UpdateNotifierApps
	if len(req.InitialApps) > 0 {
		// Convert string apps to subscriptions with wildcard collector (any collector)
		var subscriptions []NotifierAppSubscription
		for _, app := range req.InitialApps {
			subscriptions = append(subscriptions, NotifierAppSubscription{
				CollectorName: "*",
				AppName:       app,
			})
		}

		if err := s.SetNotifierApps(ctx, notifier.ID, subscriptions); err != nil {
			return fmt.Errorf("set notifier apps: %w", err)
		}

		for _, sub := range subscriptions {
			s.RegisterNotifier(sub.CollectorName, sub.AppName, instance)
		}
	}

	slog.Info("Notifier added successfully", "name", req.Name, "apps", req.InitialApps)
	return nil
}

// UpdateNotifierApps updates apps for a notifier, sends subscription notifications for new apps
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
	notifier, err := s.GetNotifier(ctx, notifierID)
	if err != nil {
		return fmt.Errorf("get notifier: %w", err)
	}

	if !notifier.Enabled {
		// Notifier disabled, skip notifications and registration
		return nil
	}

	instance, err := s.createNotifierInstance(notifier)
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
	s.clearNotifier(notifier.Name)

	// 8. Re-register for new subscriptions
	for _, sub := range newSubscriptions {
		s.RegisterNotifier(sub.CollectorName, sub.AppName, instance)
	}

	return nil
}

// createNotifierInstance creates a notifier instance from database record
func (s *Service) createNotifierInstance(notifier NotifierData) (Notifier, error) {
	switch notifier.Type {
	case "telegram":
		var cfg struct {
			Token  string `json:"token"`
			ChatID int64  `json:"chat_id"`
		}
		if err := json.Unmarshal([]byte(notifier.Config), &cfg); err != nil {
			return nil, fmt.Errorf("parse telegram config: %w", err)
		}
		return NewTelegramNotifier(notifier.Name, cfg.Token, cfg.ChatID)
	default:
		return nil, fmt.Errorf("unknown notifier type: %s", notifier.Type)
	}
}

// clearNotifier removes all registrations for a notifier by name
func (s *Service) clearNotifier(notifierName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, notifiers := range s.subscriptions {
		var filtered []Notifier
		for _, n := range notifiers {
			if n.Name() != notifierName {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) > 0 {
			s.subscriptions[key] = filtered
		} else {
			delete(s.subscriptions, key)
		}
	}
}

// LoadEnabled loads and registers enabled notifiers from the database
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

		owner, err := s.userService.GetByID(ctx, notifier.UserID)
		if err != nil {
			slog.Error("Failed to get notifier owner", "notifier_id", notifier.ID, "error", err)
			continue
		}

		if owner.IsAdmin != 1 {
			userApps, err := s.userService.GetApps(ctx, owner.ID)
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

		instance, err := s.buildNotifier(notifier)
		if err != nil {
			slog.Error("Failed to create notifier", "notifier_id", notifier.ID, "error", err)
			continue
		}

		for _, sub := range subscriptions {
			s.RegisterNotifier(sub.CollectorName, sub.AppName, instance)
		}

		slog.Info("Loaded notifier", "name", notifier.Name, "type", notifier.Type, "subscriptions", len(subscriptions), "owner", owner.Username)
	}

	return nil
}

// buildNotifier creates a notifier instance from database record
func (s *Service) buildNotifier(notifier db.Notifier) (Notifier, error) {
	switch notifier.Type {
	case "telegram":
		var cfg TelegramConfig
		if err := json.Unmarshal([]byte(notifier.Config), &cfg); err != nil {
			return nil, err
		}
		return NewTelegramNotifier(notifier.Name, cfg.Token, cfg.ChatID)
	default:
		return nil, fmt.Errorf("unknown notifier type: %s", notifier.Type)
	}
}

// RegisterNotifierWithUser registers a notifier with user-based app filtering
func (s *Service) RegisterNotifierWithUser(ctx context.Context, notifierID int64, user sharedtypes.User) {
	notifier, err := s.GetNotifierDB(ctx, notifierID)
	if err != nil {
		slog.Error("Failed to get notifier for registration", "error", err)
		return
	}

	if notifier.Enabled != 1 {
		return
	}

	subscriptions, err := s.GetNotifierApps(ctx, notifierID)
	if err != nil {
		slog.Error("Failed to get notifier subscriptions", "notifier_id", notifierID, "error", err)
		return
	}

	if len(subscriptions) == 0 {
		return
	}

	// Filter subscriptions based on user access
	if !user.IsAdmin {
		userApps, err := s.userService.GetApps(ctx, user.ID)
		if err != nil {
			slog.Error("Failed to get user apps", "user_id", user.ID, "error", err)
			return
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
		return
	}

	instance, err := s.buildNotifier(*notifier)
	if err != nil {
		slog.Error("Failed to build notifier", "notifier_id", notifierID, "error", err)
		return
	}

	for _, sub := range subscriptions {
		s.RegisterNotifier(sub.CollectorName, sub.AppName, instance)
	}

	slog.Info("Registered notifier for user", "notifier_id", notifierID, "user", user.Username, "subscriptions", len(subscriptions))
}

// UnregisterNotifierForUser removes all registrations for a notifier
func (s *Service) UnregisterNotifierForUser(ctx context.Context, notifierID int64) {
	notifier, err := s.GetNotifierDB(ctx, notifierID)
	if err != nil {
		slog.Error("Failed to get notifier for unregistration", "error", err)
		return
	}

	s.clearNotifier(notifier.Name)
	slog.Info("Unregistered notifier", "notifier_id", notifierID, "name", notifier.Name)
}

// Database methods (previously in Store)

// GetNotifierDB retrieves a notifier by ID (returns db.Notifier)
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

// GetNotifierByName retrieves a notifier by name
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

// ListNotifiers returns all notifiers
func (s *Service) ListNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.ListNotifiers(ctx)
}

// ListNotifiersByUser returns notifiers for a specific user
func (s *Service) ListNotifiersByUser(ctx context.Context, userID int64) ([]db.Notifier, error) {
	return s.queries.ListNotifiersByUser(ctx, userID)
}

// ListEnabledNotifiers returns all enabled notifiers
func (s *Service) ListEnabledNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.ListEnabledNotifiers(ctx)
}

// UpdateNotifier updates a notifier's details
func (s *Service) UpdateNotifier(ctx context.Context, id int64, name, notifierType, config string, enabled bool) error {
	enabledInt := int64(0)
	if enabled {
		enabledInt = 1
	}
	return s.queries.UpdateNotifier(ctx, db.UpdateNotifierParams{
		ID:      id,
		Name:    name,
		Type:    notifierType,
		Config:  config,
		Enabled: enabledInt,
	})
}

// DeleteNotifier removes a notifier
func (s *Service) DeleteNotifier(ctx context.Context, id int64) error {
	return s.queries.DeleteNotifier(ctx, id)
}

// EnableNotifier enables a notifier
func (s *Service) EnableNotifier(ctx context.Context, id int64) error {
	return s.queries.EnableNotifier(ctx, id)
}

// DisableNotifier disables a notifier
func (s *Service) DisableNotifier(ctx context.Context, id int64) error {
	return s.queries.DisableNotifier(ctx, id)
}

// GetNotifierApps returns app subscriptions for a notifier
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

// AddNotifierApp adds an app subscription to a notifier
func (s *Service) AddNotifierApp(ctx context.Context, notifierID int64, appName string) error {
	return s.queries.AddNotifierApp(ctx, db.AddNotifierAppParams{
		NotifierID: notifierID,
		AppName:    appName,
	})
}

// RemoveNotifierApp removes an app subscription from a notifier
func (s *Service) RemoveNotifierApp(ctx context.Context, notifierID int64, appName string) error {
	return s.queries.RemoveNotifierApp(ctx, db.RemoveNotifierAppParams{
		NotifierID: notifierID,
		AppName:    appName,
	})
}

// SetNotifierApps sets all app subscriptions for a notifier (replaces existing)
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

// GetNotifiersForApp returns notifiers subscribed to a specific app
func (s *Service) GetNotifiersForApp(ctx context.Context, appName string) ([]db.Notifier, error) {
	return s.queries.GetNotifiersForApp(ctx, appName)
}

// GetGlobalNotifiers returns notifiers subscribed to all apps
func (s *Service) GetGlobalNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.GetGlobalNotifiers(ctx)
}

// CreateNotifier creates a new notifier
func (s *Service) CreateNotifier(ctx context.Context, userID int64, name, notifierType, config string, enabled bool) (NotifierData, error) {
	enabledInt := int64(0)
	if enabled {
		enabledInt = 1
	}

	notifier, err := s.queries.CreateNotifier(ctx, db.CreateNotifierParams{
		UserID:  userID,
		Name:    name,
		Type:    notifierType,
		Config:  config,
		Enabled: enabledInt,
	})
	if err != nil {
		return NotifierData{}, err
	}

	return NotifierData{
		ID:      notifier.ID,
		UserID:  notifier.UserID,
		Name:    notifier.Name,
		Type:    notifier.Type,
		Config:  notifier.Config,
		Enabled: notifier.Enabled == 1,
	}, nil
}

// GetNotifier retrieves a notifier by ID (returns NotifierData)
func (s *Service) GetNotifier(ctx context.Context, id int64) (NotifierData, error) {
	notifier, err := s.queries.GetNotifier(ctx, id)
	if err != nil {
		return NotifierData{}, err
	}

	return NotifierData{
		ID:      notifier.ID,
		UserID:  notifier.UserID,
		Name:    notifier.Name,
		Type:    notifier.Type,
		Config:  notifier.Config,
		Enabled: notifier.Enabled == 1,
	}, nil
}
