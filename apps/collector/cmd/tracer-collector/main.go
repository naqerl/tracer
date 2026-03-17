package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"tracer/apps/collector/internal/app"
	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/formatguesser"
	"tracer/apps/collector/internal/health"
	"tracer/apps/collector/internal/install"
	"tracer/apps/collector/internal/lifecycle"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/stats"
	"tracer/apps/collector/internal/transport"
	"tracer/libs/logx"
	"tracer/libs/types"
	"tracer/libs/updater"
)

func main() {
	// Define flags
	var (
		addCmd = flag.NewFlagSet("add", flag.ExitOnError)
		setCmd = flag.NewFlagSet("set", flag.ExitOnError)

		configPath    = flag.String("config", "", "path to config file")
		printDefaults = flag.Bool("print-defaults", false, "print default configuration and exit")
	)
	flag.Parse()

	logx.SetDefault()

	// Resolve config path: flag -> env -> default
	cfgPath := resolveConfigPath(*configPath)

	if *printDefaults {
		if err := printDefaultsCmd(); err != nil {
			log.Fatalf("failed to print defaults: %s", err)
		}
		return
	}

	// Setup context with cancellation on shutdown signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if flag.NArg() > 0 {
		switch flag.Arg(0) {
		case "add":
			cfg := config.Load(cfgPath)
			handleAdd(ctx, cfgPath, cfg, addCmd)
			return
		case "set":
			cfg := config.Load(cfgPath)
			handleSet(cfgPath, cfg, setCmd)
			return
		case "update":
			handleUpdate()
			return
		case "install":
			handleInstall(flag.Args()[1:])
			return
		default:
			log.Fatalf("Unknown command: %s\n", flag.Arg(0))
		}
	}

	// Default behavior: run the collector
	cfg := config.Load(cfgPath)

	slog.Info("Starting collector",
		"name", cfg.Transport.Name,
		"panelHost", cfg.Transport.PanelHost,
		"apps", len(cfg.Apps),
	)

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

func serve(ctx context.Context, cfg config.Config) error {
	var wg sync.WaitGroup
	mpsc := make(chan types.Event, 1000)

	// Parsers
	parsers := make(map[string]parser.Parser, len(cfg.Parsers))
	for id, spec := range cfg.Parsers {
		if _, exists := parsers[id]; exists {
			return fmt.Errorf("duplicate parser id: %s", id)
		}
		spec.ID = id
		parsers[id] = parser.New(spec)
	}

	// Create apps
	apps := make(map[string]app.App, len(cfg.Apps))
	healthPollers := make(map[string]health.Poller)
	appNames := make([]string, 0, len(cfg.Apps))
	for _, spec := range cfg.Apps {
		if _, exists := apps[spec.Name]; exists {
			return fmt.Errorf("duplicate app name: %s", spec.Name)
		}

		p, ok := parsers[spec.ParserID]
		if !ok {
			return fmt.Errorf("parser with id=%s not found", spec.ParserID)
		}
		s, err := source.New(spec.Source)
		if err != nil {
			return fmt.Errorf("invalid source config for app %s: %w", spec.Name, err)
		}

		lfc, err := lifecycle.New(spec.Lifecycle)
		if err != nil {
			return fmt.Errorf("invalid lifecycle config for app %s: %w", spec.Name, err)
		}

		apps[spec.Name] = app.New(s, p, lfc, spec.Name)
		if spec.Health != nil {
			poller, err := health.New(*spec.Health)
			if err != nil {
				return fmt.Errorf("invalid health config for app %s: %w", spec.Name, err)
			}
			healthPollers[spec.Name] = poller
		}
		appNames = append(appNames, spec.Name)
	}

	// Resource monitor
	statsMonitor := stats.New(cfg.Stats)

	// Transport
	transport := transport.New(cfg.Transport, appNames, mpsc)

	// Run
	wg.Go(func() {
		slog.Info("starting transport")
		defer slog.Info("transport stopped")

		transport.Start(ctx)
	})
	wg.Go(func() {
		slog.Info("starting stats monitor")
		defer slog.Info("stats monitor stopped")

		for evt := range statsMonitor.Produce(ctx) {
			select {
			case mpsc <- evt:
			case <-ctx.Done():
				return
			}
		}
	})
	for _, app := range apps {
		wg.Go(func() {
			slog.Info("starting app producer", "app", app.Name)
			defer slog.Info("app producer stopped", "app", app.Name)

			for evt := range app.Produce(ctx) {
				select {
				case mpsc <- evt:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	for appName, poller := range healthPollers {
		wg.Go(func() {
			slog.Info("starting health poller", "app", appName)
			defer slog.Info("health poller stopped", "app", appName)

			for e := range poller.Produce(ctx) {
				select {
				case mpsc <- types.HealthEvent{
					App:    appName,
					Time:   e.Time,
					Status: e.Status,
				}:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	wg.Wait()

	return nil
}

func handleAdd(ctx context.Context, path string, cfg config.Config, addCmd *flag.FlagSet) {
	override := addCmd.Bool("override", false, "override the app unit in the config")
	healthURL := addCmd.String("health", "", "health check URL to attach as an HTTP health probe")
	ignoreFormatChange := addCmd.Bool("ignore-format-change", false, "ignore older format changes and use the latest compatible log format")
	addCmd.Parse(flag.Args()[1:])
	remaining := addCmd.Args()

	if len(remaining) == 0 {
		log.Fatal("Missing source path or systemd unit")
	}
	name, spec := inferSourceSpec(remaining[0])

	fmt.Printf("Adding log source: %s\n", remaining[0])
	if spec.Type == "file" {
		fmt.Printf("  Type: file path\n")
		fmt.Printf("  Path: %s\n", spec.Params["path"])
	} else {
		fmt.Printf("  Type: systemd unit\n")
		fmt.Printf("  Unit: %s\n", spec.Params["unit"])
	}

	if err := formatguesser.Add(ctx, &cfg, name, spec, formatguesser.AddRequest{
		Override:   *override,
		AllowMixed: *ignoreFormatChange,
	}); err != nil {
		log.Fatalf("Failed to guess the source format, error: %v", err)
	}

	// Find the app we just added/updated
	var addedApp *config.AppSpec
	for i := range cfg.Apps {
		if cfg.Apps[i].Name == name || (cfg.Apps[i].Source.Type == spec.Type &&
			cfg.Apps[i].Source.Params["unit"] == spec.Params["unit"] &&
			cfg.Apps[i].Source.Params["path"] == spec.Params["path"]) {
			addedApp = &cfg.Apps[i]
			break
		}
	}

	if addedApp == nil {
		log.Fatalf("Failed to find the added app in config")
	}

	parserSpec := cfg.Parsers[addedApp.ParserID]

	fmt.Printf("\nDetected log format:\n")
	fmt.Printf("  Parser: %s\n", parserSpec.Type)
	fmt.Printf("  Timestamp field: %s\n", parserSpec.Params["timestamp_field"])
	fmt.Printf("  Timestamp format: %s\n", parserSpec.Params["timestamp_format"])
	fmt.Printf("  Level field: %s\n", parserSpec.Params["level_field"])
	fmt.Printf("  Message field: %s\n", parserSpec.Params["message_field"])
	if traceField := parserSpec.Params["trace_id_field"]; traceField != "" {
		fmt.Printf("  Trace ID field: %s\n", traceField)
	}

	if strings.TrimSpace(*healthURL) != "" {
		fmt.Printf("\nAttaching health check: %s\n", strings.TrimSpace(*healthURL))
		attachHTTPHealth(&cfg, name, spec, strings.TrimSpace(*healthURL))
	}

	if err := cfg.Persist(path); err != nil {
		log.Fatalf("Failed to write the file config, error: %v", err)
	}

	fmt.Printf("\nConfiguration saved to: %s\n", path)
	fmt.Printf("\nAdded app configuration:\n")
	fmt.Printf("  Name: %s\n", addedApp.Name)
	fmt.Printf("  Parser ID: %s\n", addedApp.ParserID)
	fmt.Printf("  Source Type: %s\n", addedApp.Source.Type)
}

func handleSet(path string, cfg config.Config, setCmd *flag.FlagSet) {
	setCmd.Parse(flag.Args()[1:])
	remaining := setCmd.Args()

	if len(remaining) < 2 {
		log.Fatal("Usage: tracer-collector set <key> <value>")
	}

	key := remaining[0]
	value := remaining[1]

	switch key {
	case "api-key":
		cfg.Transport.APIKey = value
	case "panel-host":
		cfg.Transport.PanelHost = value
	case "name":
		cfg.Transport.Name = value
	default:
		log.Fatalf("Unknown key: %s. Supported keys: api-key, panel-host, name", key)
	}

	if err := cfg.Persist(path); err != nil {
		log.Fatalf("Failed to write the file config, error: %v", err)
	}

	fmt.Printf("Set %s successfully\n", key)
}

func handleUpdate() {
	if err := updater.Update(updater.Options{
		BinaryName: updater.BinaryCollector,
		PostUpdate: func() {
			fmt.Println("Please restart the collector to apply the update:")
			fmt.Println()
			fmt.Println("  sudo systemctl restart tracer-collector")
			fmt.Println()
		},
	}); err != nil {
		log.Fatalf("Update failed: %v", err)
	}
}

func handleInstall(args []string) {
	if err := install.Run(args); err != nil {
		log.Fatalf("Install failed: %v", err)
	}
}

func inferSourceSpec(raw string) (string, source.Spec) {
	if looksLikePath(raw) {
		return filepath.Base(raw), source.Spec{
			Type: "file",
			Params: map[string]string{
				"path":      raw,
				"read_from": "end",
			},
		}
	}

	return trimSystemdUnitSuffix(raw), source.Spec{
		Type: "journalctl",
		Params: map[string]string{
			"unit": raw,
		},
	}
}

func trimSystemdUnitSuffix(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasSuffix(trimmed, ".service") {
		return strings.TrimSuffix(trimmed, ".service")
	}
	return trimmed
}

func looksLikePath(raw string) bool {
	return strings.ContainsRune(raw, '/') || strings.HasPrefix(raw, ".")
}

func attachHTTPHealth(cfg *config.Config, name string, spec source.Spec, url string) {
	defaults := health.HTTPConfig{}.Default()
	defaults.URL = url
	healthSpec := defaults.Spec()

	for i := range cfg.Apps {
		app := &cfg.Apps[i]
		if app.Name == name || (app.Source.Type == spec.Type && app.Source.Params["unit"] == spec.Params["unit"] && app.Source.Params["path"] == spec.Params["path"]) {
			app.Health = &healthSpec
			return
		}
	}

	log.Fatalf("Failed to attach health config: app %q was not found after add", name)
}

// resolveConfigPath resolves the config path from flag, env var, or default.
// Priority: flag > env var > default from install package.
func resolveConfigPath(flagPath string) string {
	if flagPath != "" {
		return flagPath
	}
	if envPath := os.Getenv(install.ConfigEnvVar); envPath != "" {
		return envPath
	}
	return install.ConfigFilePath
}
