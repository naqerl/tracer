package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-shift/tracer/config"
	"github.com/ai-shift/tracer/core/services/auth"
	"github.com/ai-shift/tracer/core/services/dbmanager"
	"github.com/ai-shift/tracer/core/services/notify"
	"github.com/ai-shift/tracer/core/services/retention"
	"github.com/ai-shift/tracer/web"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	// Setup structured logging
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Load configuration
	cfg, err := config.LoadPanel(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("Starting panel",
		"port", cfg.Port,
		"data_dir", cfg.DataDir,
		"sample_rate", cfg.SampleRate,
		"retention_days", cfg.RetentionDays,
	)

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize database manager
	dbManager, err := dbmanager.New(cfg.DataDir)
	if err != nil {
		slog.Error("Failed to create database manager", "error", err)
		os.Exit(1)
	}
	defer dbManager.Close()

	// Initialize auth service
	sessionDuration := cfg.SessionDurationParsed()
	authService, err := auth.New(cfg.DataDir, sessionDuration)
	if err != nil {
		slog.Error("Failed to create auth service", "error", err)
		os.Exit(1)
	}
	defer authService.Close()

	// Check if first run (no users exist)
	hasUsers, err := authService.HasUsers(ctx)
	if err != nil {
		slog.Error("Failed to check users", "error", err)
		os.Exit(1)
	}

	if !hasUsers {
		slog.Info("No users found. Visit /setup to create the admin account.",
			"url", fmt.Sprintf("http://localhost:%s/setup", cfg.Port))
	} else {
		// Ensure API key exists (only log if users exist, otherwise setup page will show it)
		apiKey, err := authService.EnsureAPIKey(ctx)
		if err != nil {
			slog.Error("Failed to ensure API key", "error", err)
			os.Exit(1)
		}
		slog.Info("API key ready", "key", apiKey)
	}

	// Initialize notification service
	notifyService := notify.New()
	go notifyService.Start(ctx)

	// Load and register enabled notifiers from database
	if err := loadNotifiers(ctx, authService, notifyService); err != nil {
		slog.Error("Failed to load notifiers", "error", err)
		// Continue anyway - notifiers can be added via UI
	}

	// Initialize retention service
	retentionService := retention.New(dbManager, cfg.RetentionDays)
	go retentionService.Start(ctx)

	// Start session cleanup goroutine
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := authService.CleanupExpiredSessions(ctx); err != nil {
					slog.Error("Failed to cleanup sessions", "error", err)
				}
			}
		}
	}()

	// Initialize HTTP server
	sessionDurationSec := int(sessionDuration.Seconds())
	server := web.Run(cfg.Port, dbManager, cfg.SampleRate, notifyService, authService, sessionDurationSec)

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		slog.Info("Received signal, shutting down", "signal", sig)
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("Server shutdown error", "error", err)
		}
	}()

	// Start server
	slog.Info("Server starting", "addr", ":"+cfg.Port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("Server error", "error", err)
		os.Exit(1)
	}

	slog.Info("Server stopped")
}

// TelegramConfig represents Telegram notifier configuration
type TelegramConfig struct {
	Token  string `json:"token"`
	ChatID int64  `json:"chat_id"`
}

// loadNotifiers loads enabled notifiers from the database and registers them
func loadNotifiers(ctx context.Context, authService *auth.Service, notifyService *notify.Service) error {
	notifiers, err := authService.ListNotifiers(ctx)
	if err != nil {
		return fmt.Errorf("failed to list notifiers: %w", err)
	}

	for _, notifier := range notifiers {
		if notifier.Enabled != 1 {
			continue
		}

		apps, err := authService.GetNotifierApps(ctx, notifier.ID)
		if err != nil {
			slog.Error("Failed to get notifier apps", "notifier_id", notifier.ID, "error", err)
			continue
		}

		if len(apps) == 0 {
			continue // No apps configured, skip
		}

		// Get the owner user to check if they're admin
		owner, err := authService.GetUserByID(ctx, notifier.UserID)
		if err != nil {
			slog.Error("Failed to get notifier owner", "notifier_id", notifier.ID, "error", err)
			continue
		}

		// For non-admin users, filter apps to only those they have access to
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
			var filtered []string
			for _, app := range apps {
				if app == "*" {
					// Non-admins can't use wildcard
					continue
				}
				if userAppMap[app] {
					filtered = append(filtered, app)
				}
			}
			apps = filtered
		}

		if len(apps) == 0 {
			continue // No accessible apps after filtering
		}

		var n notify.Notifier
		switch notifier.Type {
		case "telegram":
			var cfg TelegramConfig
			if err := json.Unmarshal([]byte(notifier.Config), &cfg); err != nil {
				slog.Error("Failed to parse telegram config", "notifier_id", notifier.ID, "error", err)
				continue
			}
			tg, err := notify.NewTelegramNotifier(notifier.Name, cfg.Token, cfg.ChatID)
			if err != nil {
				slog.Error("Failed to create telegram notifier", "notifier_id", notifier.ID, "error", err)
				continue
			}
			n = tg
		default:
			slog.Warn("Unknown notifier type", "type", notifier.Type, "notifier_id", notifier.ID)
			continue
		}

		// Register for each app
		for _, app := range apps {
			notifyService.RegisterNotifier(app, n)
		}

		slog.Info("Loaded notifier", "name", notifier.Name, "type", notifier.Type, "apps", apps, "owner", owner.Username)
	}

	return nil
}
