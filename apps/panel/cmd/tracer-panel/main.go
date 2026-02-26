package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/retention"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/notify"
	"tracer/apps/panel/internal/services/notify/telegram"
	"tracer/apps/panel/internal/services/user"
	panelmigrations "tracer/apps/panel/sql/panel"
	"tracer/apps/panel/web"
	"tracer/libs/logx"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	logx.SetDefault()

	cfg := config.Load(*configPath)

	slog.Info("Starting panel",
		"port", cfg.Port,
		"dataDir", cfg.DataDir,
		"sampleRate", cfg.SampleRate,
		"retentionDays", cfg.RetentionDays,
	)

	// Setup context with cancellation on shutdown signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, cfg); err != nil {
		log.Fatalf("failed to serve: %s", err)
	}
}

func serve(ctx context.Context, cfg config.Panel) error {
	var wg sync.WaitGroup

	// Initialize database manager
	dbManager, err := dbmanager.New(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("create database manager: %w", err)
	}
	defer dbManager.Close()

	// Initialize panel database and run migrations
	panelDB, err := initPanelDB(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("initialize panel database: %w", err)
	}
	defer panelDB.Close()

	// Initialize services
	authService := auth.New(panelDB)
	userService := user.New(panelDB)

	// Check if first run (no users exist)
	hasUsers, err := userService.HasAny(ctx)
	if err != nil {
		return fmt.Errorf("check users: %w", err)
	}

	if !hasUsers {
		slog.Info("No users found. Visit /setup to create the admin account.",
			"url", fmt.Sprintf("http://localhost:%s/setup", cfg.Port))
	} else {
		// Ensure API key exists (only log if users exist, otherwise setup page will show it)
		apiKey, err := authService.EnsureAPIKey(ctx)
		if err != nil {
			return fmt.Errorf("ensure API key: %w", err)
		}
		slog.Info("API key ready", "key", apiKey)
	}

	// Setup notifier factories
	notifierFactories := map[string]notify.FromRawJSON{
		"telegram": telegram.FromRawJSON,
	}

	// Create notification channel (shared between dispatcher and ingest service)
	notifyChan := make(chan notify.Notification, 100)

	// Initialize notification components
	notifyDispatcher := notify.NewDispatcher(panelDB, notifierFactories, notifyChan)
	notifyService := notify.New(panelDB, userService, notifierFactories)

	// Initialize ingest service
	alertFormatter := func(collectorName, appName string, logID int64) string {
		if cfg.PanelURL == "" {
			return ""
		}
		return fmt.Sprintf("%s/panel/collectors/%s/apps/%s/logs/%d", cfg.PanelURL, collectorName, appName, logID)
	}
	ingestService := ingest.New(dbManager, notifyChan, alertFormatter)

	// Initialize retention service
	retentionService := retention.New(dbManager, ingestService, cfg.RetentionDays)

	// Initialize collector tracker
	tracker := ingest.NewTracker()

	// Run services
	wg.Go(func() { notifyDispatcher.Start(ctx) })
	wg.Go(func() { retentionService.Start(ctx) })
	wg.Go(func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tracker.Cleanup(1 * time.Hour)
				if err := authService.CleanupExpiredSessions(ctx); err != nil {
					slog.Error("Failed to cleanup sessions", "error", err)
				}
			}
		}
	})

	// Initialize HTTP server
	deps := web.Dependencies{
		DBManagerx: dbManager,
		Ingest:     ingestService,
		Notify:     notifyService,
		Auth:       authService,
		User:       userService,
		Tracker:    tracker,
	}
	server := web.Run(deps, cfg.Port, cfg.SampleRate)

	// Handle server shutdown
	wg.Go(func() {
		<-ctx.Done()
		slog.Info("Shutting down server")

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Error("Server shutdown error", "error", err)
		}
	})

	// Start server
	slog.Info("Server starting", "addr", ":"+cfg.Port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	wg.Wait()
	slog.Info("Server stopped")

	return nil
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
