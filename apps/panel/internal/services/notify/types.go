package notify

import (
	"fmt"
	"time"

	"tracer/libs/types"
)

// NotificationType represents the type/severity of notification.
type NotificationType string

const (
	TypeAlert         NotificationType = "alert"           // Error alerts requiring attention
	TypeLifecycle     NotificationType = "lifecycle"       // App lifecycle notifications (start/restart/exit)
	TypeWelcome       NotificationType = "welcome"         // Welcome message for new notifiers
	TypeSubscription  NotificationType = "subscription"    // Subscription confirmation
	TypeNewErrorGroup NotificationType = "new_error_group" // First error entry of a new log group
)

// LogEntry carries the structured log data behind a log-derived notification
// (TypeAlert, TypeNewErrorGroup). Notifiers render their own presentation
// from these fields.
type LogEntry struct {
	Level     types.LogLevel
	Message   string
	GroupID   int64
	LogID     int64
	CreatedAt time.Time
	LogURL    string // panel link to the log entry
}

// Notification represents a notification to be sent. Payload's concrete type
// is tied to Type: LogEntry for TypeAlert and TypeNewErrorGroup, string for
// the remaining types.
type Notification struct {
	Type          NotificationType
	CollectorName string
	AppName       string
	Payload       any
}

// FormatMessage renders the conventional plain-text presentation used by
// chat-style notifiers (ntfy, telegram): an app@collector header, and either
// the log message in a code fence with its panel link (LogEntry payload) or
// the plain payload string.
func FormatMessage(n Notification) string {
	var msg string
	switch p := n.Payload.(type) {
	case LogEntry:
		msg = fmt.Sprintf("```\n%s\n```", p.Message)
		if p.LogURL != "" {
			msg += fmt.Sprintf("\n\n[view log](%s)", p.LogURL)
		}
	case string:
		msg = p
	default:
		return ""
	}
	if n.AppName != "" || n.CollectorName != "" {
		header := n.AppName
		if n.CollectorName != "" {
			header = fmt.Sprintf("%s@%s", n.AppName, n.CollectorName)
		}
		msg = fmt.Sprintf("%s\n\n%s", header, msg)
	}
	return msg
}

// Notifier is the interface that all notification providers must implement.
type Notifier interface {
	// Type returns the notifier type (e.g., "telegram", "slack", "email")
	Type() string
	// Name returns the user-defined name for this notifier instance
	Name() string
	// Notify sends a notification
	Notify(n Notification) error
}

// FromRawJSON is a function that creates a Notifier from name and config string.
// The config string is typically JSON specific to the notifier type.
type FromRawJSON func(name, config string) (Notifier, error)
