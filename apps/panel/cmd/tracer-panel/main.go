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

	panelbuild "tracer/apps/panel"
	"tracer/apps/panel/internal/cli"
	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/install"
	"tracer/apps/panel/internal/retention"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/notify"
	"tracer/apps/panel/internal/services/notify/forgejo"
	"tracer/apps/panel/internal/services/notify/ntfy"
	"tracer/apps/panel/internal/services/notify/telegram"
	"tracer/apps/panel/internal/services/panel"
	"tracer/apps/panel/internal/services/topology"
	"tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/web"
	"tracer/build"
	"tracer/libs/installx"
	"tracer/libs/logx"
	"tracer/libs/updatex"
)

func main() {
	// Define flags
	var (
		configPath     = flag.String("config", "", "path to config file")
		printDefaults  = flag.Bool("print-defaults", false, "print default configuration and exit")
		generateAPIKey = flag.Bool("generate-api-key", false, "generate and persist a new collector API key, output raw key and exit")
		initPanel      = flag.Bool("init", false, "initialize panel: create admin user, generate collector API key, output credentials")
		initForce      = flag.Bool("force", false, "with -init: overwrite the password of the given admin user if the panel is already initialized")
		initUsername   = flag.String("username", "admin", "admin username for -init")
		initPassword   = flag.String("password", "", "admin password for -init (auto-generated if empty)")
		keyName        = flag.String("key-name", "default", "collector API key name for -generate-api-key")
		noAuth         = flag.Bool("no-auth", false, "disable authentication, inject admin user for all requests")
		readOnly       = flag.Bool("readonly", false, "allow only GET requests")
		printVersion   = flag.Bool("version", false, "print version and exit")
		setCmd         = flag.NewFlagSet("set", flag.ExitOnError)
	)
	flag.Parse()

	if *printVersion {
		fmt.Printf("tracer-panel %s\n", panelbuild.Version)
		return
	}

	if *initForce && !*initPanel {
		log.Fatal("-force requires -init")
	}

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
		if err := initPanelCmd(*initUsername, *initPassword, *initForce); err != nil {
			if errors.Is(err, cli.ErrAlreadyInitialized) {
				fmt.Fprintln(os.Stderr, "Panel already initialized")
				os.Exit(2)
			}
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

	installx.RequireServiceUser(build.PanelUser, build.TracerGroup)

	slog.Info("Starting panel",
		"port", cfg.Port,
		"dataDir", cfg.DataDir,
		"sampleRate", cfg.SampleRate,
		"retentionDays", cfg.RetentionDays,
	)

	// Setup context with cancellation on shutdown signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, cfgObj, *noAuth, *readOnly); err != nil {
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

// openAuthService opens the panel database, loads (or creates) the master key,
// ensures the data dir files are owned by the service user, and constructs the
// auth service. The bcrypt work factor is set here, once, and the returned
// service is shared by all CLI commands. The caller owns the returned handle.
func openAuthService() (*dbmanager.DBHandle, *auth.Service, error) {
	handle, err := dbmanager.OpenPanelDB(config.DataDir())
	if err != nil {
		return nil, nil, fmt.Errorf("open panel database: %w", err)
	}

	// Load (or create) the master key used to encrypt API key plaintext at rest.
	keySecret, err := auth.LoadKeySecret(config.DataDir())
	if err != nil {
		handle.Close()
		return nil, nil, fmt.Errorf("load key secret: %w", err)
	}

	// Ensure data dir files (including the freshly created keys.key) are owned
	// by the service user. These commands may run as root during installation,
	// but the panel service runs as a non-root user and needs to read the key
	// file; otherwise startup would fail.
	if err := installx.ChownDirRecursive(config.DataDir(), build.PanelUser, build.TracerGroup); err != nil {
		handle.Close()
		return nil, nil, fmt.Errorf("chown data dir: %w", err)
	}

	return handle, auth.New(handle, keySecret, auth.DefaultBcryptCost), nil
}

func generateAPIKeyCmd(name string) error {
	ctx := context.Background()

	handle, authSvc, err := openAuthService()
	if err != nil {
		return err
	}
	defer handle.Close()

	apiKey, err := cli.GenerateCollectorAPIKey(ctx, authSvc, name)
	if err != nil {
		return err
	}

	fmt.Println(apiKey)
	return nil
}

func initPanelCmd(username, password string, force bool) error {
	ctx := context.Background()

	handle, authSvc, err := openAuthService()
	if err != nil {
		return err
	}
	defer handle.Close()

	result, err := cli.InitPanel(ctx, authSvc, cli.InitOptions{
		Username: username,
		Password: password,
		Force:    force,
	})
	if err != nil {
		return err
	}

	fmt.Println("admin_username=" + result.AdminUsername)
	fmt.Println("admin_password=" + result.AdminPassword)
	fmt.Println("collector_api_key=" + result.CollectorAPIKey)
	return nil
}

func serve(ctx context.Context, cfg *config.Config, noAuth, readOnly bool) error {
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

	// Load (or create) the master key used to encrypt API key plaintext at rest.
	keySecret, err := auth.LoadKeySecret(cfg.GetDataDir())
	if err != nil {
		return fmt.Errorf("load key secret: %w", err)
	}

	// Initialize services
	authService := auth.New(panelHandle, keySecret, auth.DefaultBcryptCost)
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
		collectorKeys, err := authService.ListCollectorAPIKeys(ctx, domain.System{})
		if err != nil {
			return fmt.Errorf("list collector api keys: %w", err)
		}
		slog.Info("Collector API keys loaded", "count", len(collectorKeys))
	}

	// Setup notifier factories. The forgejo cache is created once and shared
	// by every forgejo notifier instance (the dispatcher builds a fresh
	// notifier per notification, so per-instance state would be lost).
	forgejoCache := forgejo.NewCache(forgejo.IssueCacheTTL)
	notifierFactories := map[string]notify.FromRawJSON{
		"telegram": telegram.FromRawJSON,
		"ntfy":     ntfy.FromRawJSON,
		"forgejo": func(name, config string) (notify.Notifier, error) {
			return forgejo.New(name, config, forgejoCache)
		},
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
	panelService := panel.New(dbManager, ingestService, tracker, userService)
	topologyService := topology.NewService(panelService)

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

func handleUpdate() {
	if err := updatex.Update(updatex.Options{
		BinaryName: updatex.BinaryPanel,
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
