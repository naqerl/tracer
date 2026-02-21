package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
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

// Service manages notifications and dispatches them to appropriate notifiers
type Service struct {
	ch        chan Notification
	semaphore chan struct{}

	mu            sync.RWMutex
	subscriptions map[CollectorApp][]Notifier // (collector, app) -> notifiers
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

// NotifierRepository provides access to notifier database operations
// This interface prevents circular dependencies with the auth package
type NotifierRepository interface {
	CreateNotifier(ctx context.Context, userID int64, name, notifierType, config string, enabled bool) (NotifierData, error)
	GetNotifier(ctx context.Context, id int64) (NotifierData, error)
	SetNotifierApps(ctx context.Context, notifierID int64, subscriptions []NotifierAppSubscription) error
	GetNotifierApps(ctx context.Context, notifierID int64) ([]NotifierAppSubscription, error)
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

// New creates a new notification service
func New() *Service {
	return &Service{
		ch:            make(chan Notification, channelSize),
		semaphore:     make(chan struct{}, maxWorkers),
		subscriptions: make(map[CollectorApp][]Notifier),
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
func (s *Service) AddNotifier(ctx context.Context, req AddNotifierRequest, authService NotifierRepository) error {
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
	notifier, err := authService.CreateNotifier(ctx, req.UserID, req.Name, req.Type, configJSON, true)
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

		if err := authService.SetNotifierApps(ctx, notifier.ID, subscriptions); err != nil {
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
func (s *Service) UpdateNotifierApps(ctx context.Context, notifierID int64, userID int64, newSubscriptions []NotifierAppSubscription, authService NotifierRepository) error {
	// 1. Get current subscriptions
	currentSubscriptions, err := authService.GetNotifierApps(ctx, notifierID)
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
	if err := authService.SetNotifierApps(ctx, notifierID, newSubscriptions); err != nil {
		return fmt.Errorf("update notifier subscriptions: %w", err)
	}

	// 5. Get notifier and create instance
	notifier, err := authService.GetNotifier(ctx, notifierID)
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
