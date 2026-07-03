package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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
	panelsvcupdate "tracer/apps/panel/internal/services/update"
	"tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/web"
	"tracer/build"
	"tracer/libs/logx"
	sysinstall "tracer/libs/shared/install"
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
		noAuth         = flag.Bool("no-auth", false, "disable authentication, inject admin user for all requests")
		readOnly       = flag.Bool("readonly", false, "allow only GET requests")
		setCmd         = flag.NewFlagSet("set", flag.ExitOnError)
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
			if errors.Is(err, cli.ErrAlreadyInitialized) {
				fmt.Fprintln(os.Stderr, "Panel already initialized")
				os.Exit(2)
			}
			log.Fatalf("failed to initialize panel: %s", err)
		}
		return
	}

	// Create the update service once and inject it.
	updateSvc := panelsvcupdate.New()

	// Handle subcommands
	if flag.NArg() > 0 {
		switch flag.Arg(0) {
		case "update":
			handleUpdate(updateSvc)
			return
		case "install":
			handleInstall(flag.Args()[1:])
			return
		case "config":
			handleConfig(flag.Args()[1:])
			return
		case "set":
			handleSet(cfgPath, setCmd)
			return
		default:
			log.Fatalf("Unknown command: %s\n", flag.Arg(0))
		}
	}

	// Default behavior: run the server
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("failed to load config: %s", err)
	}
	cfgObj := config.New(cfg, cfgPath)

	sysinstall.RequireServiceUser(build.PanelUser, build.TracerGroup)

	slog.Info("Starting panel",
		"port", cfg.Port,
		"dataDir", cfg.DataDir,
		"sampleRate", cfg.SampleRate,
		"retentionDays", cfg.RetentionDays,
	)

	// Setup context with cancellation on shutdown signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, cfgObj, *noAuth, *readOnly, updateSvc); err != nil {
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

	fmt.Println("admin_username=" + result.AdminUsername)
	fmt.Println("admin_password=" + result.AdminPassword)
	fmt.Println("collector_api_key=" + result.CollectorAPIKey)
	return nil
}

func serve(ctx context.Context, cfg *config.Config, noAuth, readOnly bool, updateSvc *panelsvcupdate.Service) error {
	var wg sync.WaitGroup

	// Initialize database manager
	dbManager, err := dbmanager.New(cfg.GetDataDir())
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
	panelHandle, err := dbmanager.OpenPanelDB(cfg.GetDataDir())
	if err != nil {
		return fmt.Errorf("initialize panel database: %w", err)
	}
	defer panelHandle.Close()

	// Initialize services
	authService := auth.New(panelHandle)
	userService := user.NewService(panelHandle)

	// Check if first run (no users exist)
	hasUsers, err := authService.HasAnyUsers(ctx)
	if err != nil {
		return fmt.Errorf("check users: %w", err)
	}

	if !hasUsers {
		slog.Info("No users found. Visit /setup to create the admin account.",
			"url", fmt.Sprintf("http://localhost:%s/setup", cfg.GetPort()))
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
	notifyDispatcher := notify.NewDispatcher(panelHandle, notifierFactories, notifyChan, userService)
	notifyService := notify.New(panelHandle, userService, notifierFactories)

	// Initialize ingest service
	alertFormatter := func(collectorName, appName string, logID int64) string {
		return fmt.Sprintf("%s/panel/collectors/%s/apps/%s/logs/%d", cfg.GetHost(), collectorName, appName, logID)
	}
	ingestService := ingest.New(dbManager, notifyChan, alertFormatter, cfg)

	// Initialize retention service
	retentionService := retention.New(dbManager, ingestService, cfg)

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
		UpdateSvc:    updateSvc,
		Config:       cfg,
	}
	server := web.NewServer(deps, cfg.GetPort(), noAuth, readOnly)

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
	slog.Info("Server starting", "addr", ":"+cfg.GetPort())
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server error: %w", err)
	}

	wg.Wait()
	slog.Info("Server stopped")

	return nil
}

func handleUpdate(updateSvc *panelsvcupdate.Service) {
	if err := updateSvc.UpdatePanel(); err != nil {
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

func handleSet(cfgPath string, setCmd *flag.FlagSet) {
	setCmd.Parse(flag.Args()[1:])
	remaining := setCmd.Args()

	if len(remaining) < 2 {
		log.Fatal("Usage: tracer-panel set <key> <value>")
	}

	key := remaining[0]
	value := remaining[1]

	cfg, err := config.LoadRaw(cfgPath)
	if err != nil {
		log.Fatalf("Failed to load config: %s", err)
	}

	switch key {
	case "host":
		cfg.Host = value
	case "port":
		cfg.Port = value
	case "data_dir":
		cfg.DataDir = value
	case "cpu_delta_percent":
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			log.Fatalf("Invalid value for cpu_delta_percent: %s", value)
		}
		cfg.CPUDeltaPercent = v
	case "mem_delta_kb":
		v, err := strconv.Atoi(value)
		if err != nil {
			log.Fatalf("Invalid value for mem_delta_kb: %s", value)
		}
		cfg.MemDeltaKB = v
	case "disk_delta_kb":
		v, err := strconv.Atoi(value)
		if err != nil {
			log.Fatalf("Invalid value for disk_delta_kb: %s", value)
		}
		cfg.DiskDeltaKB = v
	default:
		log.Fatalf("Unknown key: %s. Supported keys: host, port, data_dir, cpu_delta_percent, mem_delta_kb, disk_delta_kb", key)
	}

	if err := cfg.Persist(cfgPath); err != nil {
		log.Fatalf("Failed to write config: %s", err)
	}

	fmt.Printf("Set %s successfully\n", key)
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
