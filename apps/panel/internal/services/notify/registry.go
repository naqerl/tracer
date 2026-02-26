package notify

import (
	"log/slog"
	"sync"
)

const allAppsKey = "*" // Special key meaning "all apps"

// CollectorApp represents a collector-app pair for subscription routing.
type CollectorApp struct {
	CollectorName string
	AppName       string
}

// Registry manages in-memory subscription of notifiers to collector-app pairs.
// It supports wildcard matching for flexible subscription patterns.
type Registry struct {
	mu   sync.RWMutex
	subs map[CollectorApp][]Notifier
}

// NewRegistry creates a new subscription registry.
func NewRegistry() *Registry {
	return &Registry{
		subs: make(map[CollectorApp][]Notifier),
	}
}

// Register adds a notifier for a specific collector-app combination.
// Use "*" as appName to register for all apps from a collector.
// Use "*" as collectorName to register for an app from all collectors.
// Use "*" for both to register for all apps from all collectors.
func (r *Registry) Register(collectorName, appName string, notifier Notifier) {
	if collectorName == "" || appName == "" {
		slog.Error("Cannot register notifier with empty collector or app name",
			"collector", collectorName, "app", appName, "notifier", notifier.Name())
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	key := CollectorApp{CollectorName: collectorName, AppName: appName}
	r.subs[key] = append(r.subs[key], notifier)

	slog.Info("Notifier registered",
		"type", notifier.Type(),
		"name", notifier.Name(),
		"collector", collectorName,
		"app", appName)
}

// Unregister removes a notifier by name for a collector-app pair.
func (r *Registry) Unregister(collectorName, appName, notifierName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := CollectorApp{CollectorName: collectorName, AppName: appName}
	notifiers := r.subs[key]

	for i, n := range notifiers {
		if n.Name() == notifierName {
			r.subs[key] = append(notifiers[:i], notifiers[i+1:]...)
			slog.Info("Notifier unregistered",
				"name", notifierName,
				"collector", collectorName,
				"app", appName)
			return
		}
	}
}

// Clear removes all notifiers for a collector-app pair.
func (r *Registry) Clear(collectorName, appName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.subs, CollectorApp{CollectorName: collectorName, AppName: appName})
}

// ClearByName removes all registrations for a notifier by name across all collector-app pairs.
func (r *Registry) ClearByName(notifierName string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for key, notifiers := range r.subs {
		var filtered []Notifier
		for _, n := range notifiers {
			if n.Name() != notifierName {
				filtered = append(filtered, n)
			}
		}
		if len(filtered) > 0 {
			r.subs[key] = filtered
		} else {
			delete(r.subs, key)
		}
	}
}

// GetNotifiers returns all notifiers that should receive notifications for a collector-app pair.
// It handles four levels of matching:
// 1. Exact match: collector + app
// 2. Any collector for this app (collector = "*")
// 3. All apps from this specific collector (app = "*", collector = specific)
// 4. All apps from any collector (app = "*", collector = "*")
func (r *Registry) GetNotifiers(collectorName, appName string) []Notifier {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Notifier

	// 1. Exact match: collector + app
	key := CollectorApp{CollectorName: collectorName, AppName: appName}
	if notifiers, ok := r.subs[key]; ok {
		result = append(result, notifiers...)
	}

	// 2. Any collector for this app (collector = "*")
	keyAnyCollector := CollectorApp{CollectorName: allAppsKey, AppName: appName}
	if notifiers, ok := r.subs[keyAnyCollector]; ok {
		result = append(result, notifiers...)
	}

	// 3. All apps from this specific collector (app = "*", collector = specific)
	keyCollectorWildcard := CollectorApp{CollectorName: collectorName, AppName: allAppsKey}
	if notifiers, ok := r.subs[keyCollectorWildcard]; ok {
		result = append(result, notifiers...)
	}

	// 4. All apps from any collector (app = "*", collector = "*")
	keyGlobalWildcard := CollectorApp{CollectorName: allAppsKey, AppName: allAppsKey}
	if notifiers, ok := r.subs[keyGlobalWildcard]; ok {
		result = append(result, notifiers...)
	}

	return result
}
