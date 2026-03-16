package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"tracer/apps/collector/internal/app"
	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/formatguesser"
	"tracer/apps/collector/internal/health"
	"tracer/apps/collector/internal/lifecycle"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/stats"
	"tracer/apps/collector/internal/transport"
	"tracer/libs/logx"
	"tracer/libs/types"
)

func main() {
	// Define flags
	var (
		addCmd = flag.NewFlagSet("add", flag.ExitOnError)

		configPath    = flag.String("config", "", "path to config file")
		printDefaults = flag.Bool("print-defaults", false, "print default configuration and exit")
	)
	flag.Parse()

	logx.SetDefault()

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
			cfg := config.Load(*configPath)
			handleAdd(ctx, *configPath, cfg, addCmd)
			return
		default:
			log.Fatalf("Unknown command: %s\n", flag.Arg(0))
		}
	}

	// Default behavior: run the collector
	cfg := config.Load(*configPath)

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
	addCmd.Parse(flag.Args()[1:])
	remaining := addCmd.Args()

	if len(remaining) == 0 {
		log.Fatal("Missing source path or systemd unit")
	}
	name, spec := inferSourceSpec(remaining[0])

	if err := formatguesser.Add(ctx, &cfg, name, spec, *override); err != nil {
		log.Fatalf("Failed to guess the source format, error: %v", err)
	}
	if strings.TrimSpace(*healthURL) != "" {
		attachHTTPHealth(&cfg, name, spec, strings.TrimSpace(*healthURL))
	}
	if err := cfg.Persist(path); err != nil {
		log.Fatalf("Failed to write the file config, error: %v", err)
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
