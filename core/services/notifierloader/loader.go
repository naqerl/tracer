package notifierloader

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ai-shift/tracer/core/services/auth"
	"github.com/ai-shift/tracer/core/services/notifierstore"
	"github.com/ai-shift/tracer/core/services/notify"
	"github.com/ai-shift/tracer/db"
)

type TelegramConfig struct {
	Token  string `json:"token"`
	ChatID int64  `json:"chat_id"`
}

func LoadEnabled(ctx context.Context, notifierStore *notifierstore.Store, authService *auth.Service, notifyService *notify.Service) error {
	notifiers, err := notifierStore.ListNotifiers(ctx)
	if err != nil {
		return err
	}

	for _, notifier := range notifiers {
		if notifier.Enabled != 1 {
			continue
		}

		subscriptions, err := notifierStore.GetNotifierApps(ctx, notifier.ID)
		if err != nil {
			slog.Error("Failed to get notifier subscriptions", "notifier_id", notifier.ID, "error", err)
			continue
		}

		if len(subscriptions) == 0 {
			continue
		}

		owner, err := authService.GetUserByID(ctx, notifier.UserID)
		if err != nil {
			slog.Error("Failed to get notifier owner", "notifier_id", notifier.ID, "error", err)
			continue
		}

		if owner.IsAdmin != 1 {
			userApps, err := authService.GetUserApps(ctx, owner.ID)
			if err != nil {
				slog.Error("Failed to get user apps", "user_id", owner.ID, "error", err)
				continue
			}
			userAppMap := make(map[string]bool)
			for _, app := range userApps {
				userAppMap[app] = true
			}
			var filtered []notify.NotifierAppSubscription
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

		instance, err := BuildNotifier(notifier)
		if err != nil {
			slog.Error("Failed to create notifier", "notifier_id", notifier.ID, "error", err)
			continue
		}

		for _, sub := range subscriptions {
			notifyService.RegisterNotifier(sub.CollectorName, sub.AppName, instance)
		}

		slog.Info("Loaded notifier", "name", notifier.Name, "type", notifier.Type, "subscriptions", len(subscriptions), "owner", owner.Username)
	}

	return nil
}

func RegisterNotifierWithUser(ctx context.Context, notifierStore *notifierstore.Store, notifyService *notify.Service, authService *auth.Service, notifierID int64, user *db.User) {
	notifier, err := notifierStore.GetNotifierDB(ctx, notifierID)
	if err != nil {
		slog.Error("Failed to get notifier for registration", "error", err)
		return
	}

	if notifier.Enabled != 1 {
		return
	}

	subscriptions, err := notifierStore.GetNotifierApps(ctx, notifierID)
	if err != nil {
		slog.Error("Failed to get notifier subscriptions", "error", err)
		return
	}

	if user != nil && user.IsAdmin != 1 {
		userApps, err := authService.GetUserApps(ctx, user.ID)
		if err != nil {
			slog.Error("Failed to get user apps", "error", err)
			return
		}
		userAppMap := make(map[string]bool)
		for _, app := range userApps {
			userAppMap[app] = true
		}
		var filtered []notify.NotifierAppSubscription
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

	instance, err := BuildNotifier(*notifier)
	if err != nil {
		slog.Error("Failed to create notifier", "error", err)
		return
	}

	for _, sub := range subscriptions {
		notifyService.RegisterNotifier(sub.CollectorName, sub.AppName, instance)
	}
}

func BuildNotifier(notifier db.Notifier) (notify.Notifier, error) {
	switch notifier.Type {
	case "telegram":
		var cfg TelegramConfig
		if err := json.Unmarshal([]byte(notifier.Config), &cfg); err != nil {
			return nil, err
		}
		return notify.NewTelegramNotifier(notifier.Name, cfg.Token, cfg.ChatID)
	default:
		return nil, fmt.Errorf("unknown notifier type: %s", notifier.Type)
	}
}
