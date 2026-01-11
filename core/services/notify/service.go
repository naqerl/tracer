package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const (
	maxWorkers  = 5
	channelSize = 100
	allAppsKey  = "*" // Special key meaning "all apps"
)

// Service manages notifications and dispatches them to appropriate notifiers
type Service struct {
	ch        chan Notification
	semaphore chan struct{}

	mu           sync.RWMutex
	appNotifiers map[string][]Notifier // app name -> notifiers (use "*" for all apps)
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

// TODO: I don't like it's name
// AuthService provides access to auth/DB operations
// This interface prevents circular dependencies
type AuthService interface {
	CreateNotifier(ctx context.Context, userID int64, name, notifierType, config string, enabled bool) (NotifierData, error)
	GetNotifier(ctx context.Context, id int64) (NotifierData, error)
	SetNotifierApps(ctx context.Context, notifierID int64, apps []string) error
	GetNotifierApps(ctx context.Context, notifierID int64) ([]string, error)
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
		ch:           make(chan Notification, channelSize),
		semaphore:    make(chan struct{}, maxWorkers),
		// TODO: Use type alias for map key
		appNotifiers: make(map[string][]Notifier),
	}
}

// Chan returns a send-only channel for submitting notifications
func (s *Service) Chan() NotificationChan {
	return s.ch
}

// RegisterNotifier registers a notifier for specific apps
// Use "*" as appName to register for all apps
func (s *Service) RegisterNotifier(appName string, notifier Notifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appNotifiers[appName] = append(s.appNotifiers[appName], notifier)
	slog.Info("Notifier registered", "type", notifier.Type(), "name", notifier.Name(), "app", appName)
}

// UnregisterNotifier removes a notifier by name for an app
func (s *Service) UnregisterNotifier(appName, notifierName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	notifiers := s.appNotifiers[appName]
	for i, n := range notifiers {
		if n.Name() == notifierName {
			s.appNotifiers[appName] = append(notifiers[:i], notifiers[i+1:]...)
			slog.Info("Notifier unregistered", "name", notifierName, "app", appName)
			return
		}
	}
}

// ClearNotifiers removes all notifiers for an app
func (s *Service) ClearNotifiers(appName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.appNotifiers, appName)
}

// getNotifiersForApp returns all notifiers that should receive notifications for an app
func (s *Service) getNotifiersForApp(appName string) []Notifier {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Notifier

	// Add app-specific notifiers
	if notifiers, ok := s.appNotifiers[appName]; ok {
		result = append(result, notifiers...)
	}

	// Add global notifiers (registered for all apps)
	if notifiers, ok := s.appNotifiers[allAppsKey]; ok {
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
			notifiers := s.getNotifiersForApp(notification.AppName)
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

// AddNotifier creates a notifier, saves to DB, sends welcome notification, and registers it
// This is the entry point for creating new notifiers from the UI
func (s *Service) AddNotifier(ctx context.Context, req AddNotifierRequest, authService AuthService) error {
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
		Type:      TypeNotice,
		Level:     "👋 welcome",
		AppName:   "tracer",
		Message:   "Cheers, love! The cavalry's here!",
		Timestamp: time.Now(),
	}

	if err := instance.Notify(welcomeNotif); err != nil {
		slog.Error("Failed to send welcome notification", "error", err)
		// Don't fail the whole operation if welcome message fails
	} else {
		slog.Info("Sent welcome notification", "notifier", req.Name)
	}

	// 5. Register notifier for apps
	if len(req.InitialApps) > 0 {
		if err := authService.SetNotifierApps(ctx, notifier.ID, req.InitialApps); err != nil {
			return fmt.Errorf("set notifier apps: %w", err)
		}

		for _, app := range req.InitialApps {
			s.RegisterNotifier(app, instance)
		}
	}

	slog.Info("Notifier added successfully", "name", req.Name, "apps", req.InitialApps)
	return nil
}

// UpdateNotifierApps updates apps for a notifier, sends subscription notifications for new apps
func (s *Service) UpdateNotifierApps(ctx context.Context, notifierID int64, userID int64, newApps []string, authService AuthService) error {
	// 1. Get current apps
	currentApps, err := authService.GetNotifierApps(ctx, notifierID)
	if err != nil {
		return fmt.Errorf("get current apps: %w", err)
	}

	// 2. Build map for comparison
	currentAppMap := make(map[string]bool)
	for _, app := range currentApps {
		currentAppMap[app] = true
	}

	// 3. Identify newly added apps
	var newlyAddedApps []string
	for _, app := range newApps {
		if !currentAppMap[app] {
			newlyAddedApps = append(newlyAddedApps, app)
		}
	}

	// 4. Update database
	if err := authService.SetNotifierApps(ctx, notifierID, newApps); err != nil {
		return fmt.Errorf("update notifier apps: %w", err)
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

	// 6. Send subscription notifications for newly added apps
	for _, app := range newlyAddedApps {
		var message string

		if app == "*" {
			message = "Subscribed to notifications from **all applications**"
		} else {
			message = fmt.Sprintf("Subscribed to notifications from **%s**", app)
		}

		subscriptionNotif := Notification{
			Type:      TypeNotice,
			Level:     "🔔 subscription",
			AppName:   app,
			Message:   message,
			Timestamp: time.Now(),
		}

		if err := instance.Notify(subscriptionNotif); err != nil {
			slog.Error("Failed to send subscription notification", "app", app, "error", err)
		} else {
			slog.Info("Sent subscription notification", "notifier", notifier.Name, "app", app)
		}
	}

	// 7. Clear old registrations
	s.clearNotifier(notifier.Name)

	// 8. Re-register for new apps
	for _, app := range newApps {
		s.RegisterNotifier(app, instance)
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

	for appName, notifiers := range s.appNotifiers {
		var filtered []Notifier
		for _, n := range notifiers {
			if n.Name() != notifierName {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) > 0 {
			s.appNotifiers[appName] = filtered
		} else {
			delete(s.appNotifiers, appName)
		}
	}
}
