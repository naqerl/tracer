package notify

import (
	"context"
	"log/slog"
	"sync"
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

// New creates a new notification service
func New() *Service {
	return &Service{
		ch:           make(chan Notification, channelSize),
		semaphore:    make(chan struct{}, maxWorkers),
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
