package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os/signal"
	"sync"
	"syscall"

	"tracer/apps/collector/internal/app"
	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/formatguesser"
	"tracer/apps/collector/internal/lifecycle"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/apps/collector/internal/stats"
	"tracer/apps/collector/internal/transport"
	"tracer/libs/logx"
	"tracer/libs/types"
)

func main() {
	// Setup context with cancellation on shutdown signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Define flags
	var (
		addCmd = flag.NewFlagSet("add", flag.ExitOnError)

		configPath    = flag.String("config", "", "path to config file")
		printDefaults = flag.Bool("print-defaults", false, "print default configuration and exit")
	)
	flag.Parse()

	// Default behavior: run the collector
	cfg := config.Load(*configPath)

	if flag.NArg() > 0 {
		switch flag.Arg(0) {
		case "add":
			handleAdd(ctx, *configPath, cfg, addCmd)
			return
		default:
			log.Fatalf("Unknown command: %s\n", flag.Arg(0))
		}
	}

	logx.SetDefault()

	// Handle CLI commands that don't require config file
	if *printDefaults {
		if err := printDefaultsCmd(); err != nil {
			log.Fatalf("failed to print defaults: %s", err)
		}
		return
	}

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
		appNames = append(appNames, spec.Name)
	}

	// Resource monitor
	statsMonitor := stats.New(cfg.Stats)

	// Transport
	transport := transport.New(cfg.Transport, appNames, mpsc)

	// Run
	wg.Go(func() { transport.Start(ctx) })
	wg.Go(func() {
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
			for evt := range app.Produce(ctx) {
				select {
				case mpsc <- evt:
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
	addCmd.Parse(flag.Args()[1:])
	src := *addCmd.String("src", "journalctl", "source of the logs")
	remaning := addCmd.Args()
	if len(remaning) == 0 {
		log.Fatal("Missing unit name")
	}

	var ss source.SourceSeeker
	switch src {
	case "journalctl":
		ss = source.NewJournalctl(source.JournalctlConfig{
			Unit: remaning[0],
		})
	default:
		log.Fatalf("Unknown source: %s", src)
	}

	if err := formatguesser.Add(ctx, &cfg, ss); err != nil {
		log.Fatalf("Failed to guess the source format, error: %v", err)
	}
	if err := cfg.Persist(path); err != nil {
		log.Fatalf("Failed to write the file config, error: %v", err)
	}
}
