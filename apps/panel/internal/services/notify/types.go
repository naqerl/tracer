package notify

// NotificationType represents the type/severity of notification
type NotificationType string

const (
	TypeAlert        NotificationType = "alert"        // Error alerts requiring attention
	TypeLifecycle    NotificationType = "lifecycle"    // App lifecycle notifications (start/restart/exit)
	TypeWelcome      NotificationType = "welcome"      // Welcome message for new notifiers
	TypeSubscription NotificationType = "subscription" // Subscription confirmation
)

// Notification represents a notification to be sent
type Notification struct {
	Type          NotificationType
	CollectorName string
	AppName       string
	Message       string
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
