package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"tracer/apps/panel/internal/cli"
	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/install"
	"tracer/apps/panel/internal/retention"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/notify"
	"tracer/apps/panel/internal/services/notify/ntfy"
	"tracer/apps/panel/internal/services/notify/telegram"
	panelsvc "tracer/apps/panel/internal/services/panel"
	topologysvc "tracer/apps/panel/internal/services/topology"
	"tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/web"
	"tracer/libs/logx"
	"tracer/libs/updater"
)

func main() {
	// Define flags
	var (
		configPath     = flag.String("config", "", "path to config file")
		printDefaults  = flag.Bool("print-defaults", false, "print default configuration and exit")
		generateAPIKey = flag.Bool("generate-api-key", false, "generate and persist a new collector API key, output raw key and exit")
		initPanel      = flag.Bool("init", false, "initialize panel: create admin user, generate collector API key, output credentials")
		initUsername   = flag.String("username", "admin", "admin username for -init")
		initPassword   = flag.String("password", "", "admin password for -init (auto-generated if empty)")
		keyName        = flag.String("key-name", "default", "collector API key name for -generate-api-key")
	)
	flag.Parse()

	logx.SetDefault()

	// Resolve config path: flag -> env -> default
	cfgPath := resolveConfigPath(*configPath)

	// Handle CLI commands that don't require config file
	if *printDefaults {
		if err := printDefaultsCmd(); err != nil {
			log.Fatalf("failed to print defaults: %s", err)
		}
		return
	}

	if *generateAPIKey {
		if err := generateAPIKeyCmd(*keyName); err != nil {
			log.Fatalf("failed to generate API key: %s", err)
		}
		return
	}

	if *initPanel {
		if err := initPanelCmd(*initUsername, *initPassword); err != nil {
			log.Fatalf("failed to initialize panel: %s", err)
		}
		return
	}

	// Handle subcommands
	if flag.NArg() > 0 {
		switch flag.Arg(0) {
		case "update":
			handleUpdate()
			return
		case "install":
			handleInstall(flag.Args()[1:])
			return
		case "config":
			handleConfig(flag.Args()[1:])
			return
		default:
			log.Fatalf("Unknown command: %s\n", flag.Arg(0))
		}
	}

	// Default behavior: run the server
	cfg := config.Load(cfgPath)

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

func printDefaultsCmd() error {
	cfg := config.Default()
	yaml, err := cfg.String()
	if err != nil {
		return err
	}
	fmt.Print(yaml)
	return nil
}

func generateAPIKeyCmd(name string) error {
	ctx := context.Background()

	apiKey, err := cli.GenerateCollectorAPIKey(ctx, name)
	if err != nil {
		return err
	}

	fmt.Println(apiKey)
	return nil
}

func initPanelCmd(username, password string) error {
	ctx := context.Background()

	result, err := cli.InitPanel(ctx, cli.InitOptions{
		Username: username,
		Password: password,
	})
	if err != nil {
		return err
	}

	fmt.Printf("admin_username=%s\n", result.AdminUsername)
	if result.IsNewPassword {
		fmt.Printf("admin_password=%s\n", result.AdminPassword)
	}
	fmt.Printf("collector_api_key=%s\n", result.CollectorAPIKey)
	return nil
}

func serve(ctx context.Context, cfg config.Panel) error {
	var wg sync.WaitGroup

	// Initialize database manager
	dbManager, err := dbmanager.New(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("create database manager: %w", err)
	}
	defer dbManager.Close()

	// Run migrations on all existing app and collector databases
	slog.Info("Running migrations on all databases...")
	if err := dbManager.RunMigrationsForAll(); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	slog.Info("Migrations complete")

	// Initialize panel database and run migrations
	panelDB, err := dbmanager.OpenPanelDB(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("initialize panel database: %w", err)
	}
	defer panelDB.Close()

	// Initialize services
	authService := auth.New(panelDB)
	userService := user.NewService(panelDB)

	// Check if first run (no users exist)
	hasUsers, err := authService.HasAnyUsers(ctx)
	if err != nil {
		return fmt.Errorf("check users: %w", err)
	}

	if !hasUsers {
		slog.Info("No users found. Visit /setup to create the admin account.",
			"url", fmt.Sprintf("http://localhost:%s/setup", cfg.Port))
	} else {
		collectorKeys, err := authService.ListCollectorAPIKeys(ctx)
		if err != nil {
			return fmt.Errorf("list collector api keys: %w", err)
		}
		slog.Info("Collector API keys loaded", "count", len(collectorKeys))
	}

	// Setup notifier factories
	notifierFactories := map[string]notify.FromRawJSON{
		"telegram": telegram.FromRawJSON,
		"ntfy":     ntfy.FromRawJSON,
	}

	// Create notification channel (shared between dispatcher and ingest service)
	notifyChan := make(chan notify.Notification, 100)

	// Initialize notification components
	notifyDispatcher := notify.NewDispatcher(panelDB, notifierFactories, notifyChan, userService)
	notifyService := notify.New(panelDB, userService, notifierFactories)

	// Initialize ingest service
	alertFormatter := func(collectorName, appName string, logID int64) string {
		return fmt.Sprintf("%s/panel/collectors/%s/apps/%s/logs/%d", cfg.Host, collectorName, appName, logID)
	}
	ingestService := ingest.New(dbManager, notifyChan, alertFormatter, cfg.SampleRate)

	// Initialize retention service
	retentionService := retention.New(dbManager, ingestService, cfg.RetentionDays)

	// Initialize collector tracker
	tracker := ingest.NewTracker()
	panelService := panelsvc.New(dbManager, ingestService, tracker, userService)
	topologyService := topologysvc.NewService(panelService)

	// Run services
	wg.Go(func() {
		slog.Info("starting notify dispatcher")
		defer slog.Info("notify dispatcher stopped")

		notifyDispatcher.Start(ctx)
	})
	wg.Go(func() {
		slog.Info("starting retention service")
		defer slog.Info("retention service stopped")

		retentionService.Start(ctx)
	})
	wg.Go(func() {
		slog.Info("starting maintenance loop")
		defer slog.Info("maintenance loop stopped")

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
		DBManagerx:   dbManager,
		Ingest:       ingestService,
		Notify:       notifyService,
		Auth:         authService,
		User:         userService,
		Tracker:      tracker,
		Topology:     topologyService,
		PanelService: panelService,
	}
	server := web.NewServer(deps, cfg.Port)

	// Handle server shutdown
	wg.Go(func() {
		slog.Info("starting server shutdown watcher")
		defer slog.Info("server shutdown watcher stopped")

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

func handleUpdate() {
	if err := updater.Update(updater.Options{
		BinaryName: updater.BinaryPanel,
		PostUpdate: func() {
			fmt.Println("Please restart the panel to apply the update:")
			fmt.Println()
			fmt.Println("  sudo systemctl restart tracer-panel")
			fmt.Println()
		},
	}); err != nil {
		log.Fatalf("Update failed: %v", err)
	}
}

func handleInstall(args []string) {
	if err := install.Run(); err != nil {
		log.Fatalf("Install failed: %v", err)
	}
}

func handleConfig(args []string) {
	if len(args) == 0 {
		log.Fatal("Usage: tracer-panel config <key>")
	}
	if err := install.RunConfig(args[0]); err != nil {
		log.Fatalf("Config failed: %v", err)
	}
}

// resolveConfigPath resolves the config path from flag, env var, or default.
// Priority: flag > env var > default from install package.
func resolveConfigPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if envPath := os.Getenv(install.ConfigPathEnv); envPath != "" {
		return envPath
	}
	return install.ConfigFilePath
}
