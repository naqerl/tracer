package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-shift/tracer/config"
	"github.com/ai-shift/tracer/core/services/alerter"
	"github.com/ai-shift/tracer/core/services/auth"
	"github.com/ai-shift/tracer/core/services/dbmanager"
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

	// Initialize alerter service
	alertService, err := alerter.New(cfg.TelegramToken, cfg.TelegramChatID)
	if err != nil {
		slog.Error("Failed to create alerter", "error", err)
		os.Exit(1)
	}
	go alertService.Start(ctx)

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
	server := web.Run(cfg.Port, dbManager, cfg.SampleRate, alertService.AlertChan, authService, sessionDurationSec)

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
