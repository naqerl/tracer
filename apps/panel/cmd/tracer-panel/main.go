package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	panelconfig "github.com/ai-shift/tracer/apps/panel/internal/config"
	"github.com/ai-shift/tracer/apps/panel/internal/services/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/services/collectors"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/apps/panel/internal/services/notify"
	"github.com/ai-shift/tracer/apps/panel/internal/retention"
	"github.com/ai-shift/tracer/apps/panel/internal/services/user"
	"github.com/ai-shift/tracer/libs/logx"
	panelmigrations "github.com/ai-shift/tracer/apps/panel/sql/panel"
	"github.com/ai-shift/tracer/apps/panel/web"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	// Setup structured logging
	logx.SetDefault()

	// Load configuration
	cfg, err := panelconfig.Load(*configPath)
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

	collectorService := collectors.New(dbManager)

	// Initialize panel database and run migrations
	panelDB, err := initPanelDB(cfg.DataDir)
	if err != nil {
		slog.Error("Failed to initialize panel database", "error", err)
		os.Exit(1)
	}
	defer panelDB.Close()

	// Initialize services
	authService := auth.New(panelDB)
	defer authService.Close()

	userService := user.New(panelDB)

	// Check if first run (no users exist)
	hasUsers, err := userService.HasAny(ctx)
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
	notifyService := notify.New(panelDB, userService)
	go notifyService.Start(ctx)

	// Load and register enabled notifiers from database
	if err := notifyService.LoadEnabled(ctx); err != nil {
		slog.Error("Failed to load notifiers", "error", err)
		// Continue anyway - notifiers can be added via UI
	}

	// Initialize retention service
	retentionService := retention.New(dbManager, collectorService, cfg.RetentionDays)
	go retentionService.Start(ctx)

	// Initialize collector tracker
	tracker := collectors.NewTracker()

	// Start collector cleanup goroutine (remove stale disconnected collectors after 1 hour)
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tracker.Cleanup(1 * time.Hour)
			}
		}
	}()

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

	// Note: System stats cleanup is handled by retention service

	// Initialize HTTP server
	deps := web.Dependencies{
		DBManagerx: dbManager,
		Collector:  collectorService,
		Notify:     notifyService,
		Auth:       authService,
		User:       userService,
		Tracker:    tracker,
	}
	server := web.Run(deps, cfg.Port, cfg.SampleRate, cfg.PanelURL)

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

func initPanelDB(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "panel.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open panel database: %w", err)
	}

	if err := runMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return sqlDB, nil
}

func runMigrations(sqlDB *sql.DB) error {
	gooseProvider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, panelmigrations.Embed)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	_, err = gooseProvider.Up(context.Background())
	if err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}
