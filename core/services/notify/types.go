package notify

import "time"

// NotificationType represents the type/severity of notification
type NotificationType string

const (
	TypeAlert  NotificationType = "alert"  // Error alerts requiring attention
	TypeNotice NotificationType = "notice" // Informational notices
)

// Notification represents a notification to be sent
type Notification struct {
	Type      NotificationType
	AppName   string // Which app this notification is from
	TraceID   string
	Level     string
	Message   string
	Source    string
	Timestamp time.Time
}

// NotificationChan is a channel for sending notifications
type NotificationChan chan<- Notification

// Notifier is the interface that all notification providers must implement
type Notifier interface {
	// Type returns the notifier type (e.g., "telegram", "slack", "email")
	Type() string
	// Name returns the user-defined name for this notifier instance
	Name() string
	// Notify sends a notification
	Notify(n Notification) error
}
