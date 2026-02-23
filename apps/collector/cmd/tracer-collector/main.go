package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/events"
	"github.com/ai-shift/tracer/apps/collector/internal/ingest"
	"github.com/ai-shift/tracer/apps/collector/internal/stats"
	"github.com/ai-shift/tracer/apps/collector/internal/supervisor"
	"github.com/ai-shift/tracer/apps/collector/internal/transport"
	"github.com/ai-shift/tracer/apps/collector/internal/watchdog"
	"github.com/ai-shift/tracer/apps/collector/internal/watchdog/source"
	"github.com/ai-shift/tracer/libs/logx"
	"github.com/ai-shift/tracer/libs/types"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	// Setup structured logging
	logx.SetDefault()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("Starting collector",
		"name", cfg.ResolvedName,
		"panelEndpoint", cfg.PanelEndpoint,
		"apps", len(cfg.Apps),
	)

	// Setup context with cancellation on shutdown signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Create shared event bus
	bus := events.NewBus(1000)

	// Build sources for watchdog (just I/O, no parsing)
	sources := make([]watchdog.Source, len(cfg.Apps))
	for i, app := range cfg.Apps {
		switch app.SourceKind {
		case "file":
			sources[i] = source.NewFile(app.Name, app.Path, app.ReadFrom)
		case "journalctl":
			sources[i] = source.NewJournalctl(app.Name, app.Unit, app.Since)
		default:
			slog.Error("Unknown source kind", "app", app.Name, "kind", app.SourceKind)
			os.Exit(1)
		}
	}

	// Initialize watchdog (dumb I/O)
	watcher := watchdog.New(sources)

	// Initialize ingest pipeline (parsing and classification stage)
	pipe, err := ingest.New(cfg.Apps)
	if err != nil {
		slog.Error("Failed to create pipeline", "error", err)
		os.Exit(1)
	}

	// Collect app names from config
	appNames := make([]string, len(cfg.Apps))
	for i, app := range cfg.Apps {
		appNames[i] = app.Name
	}

	// Initialize WebSocket client
	wsClient := transport.New(cfg.PanelEndpoint, cfg.APIKey, cfg.ResolvedName, appNames, bus)

	// Create channels for pipeline stages
	rawLines := make(chan types.RawLine, 100)
	events := make(chan types.Event, 100)

	// Start stats producer if interval is set
	if cfg.ResolvedStatsInterval > 0 {
		statsCollector := stats.New(cfg.ResolvedDataDir)
		statsProducer := stats.NewProducer(statsCollector, cfg.ResolvedStatsInterval, bus)
		go statsProducer.Start(ctx)
		slog.Info("System stats enabled", "interval", cfg.ResolvedStatsInterval, "data_dir", cfg.ResolvedDataDir)
	}

	// Start supervisor to monitor sources and emit error events
	super := supervisor.New(sources, bus)
	go super.Run(ctx)

	// Start pipeline stages
	// Stage 1: Watchdog reads sources -> raw lines
	go func() {
		watcher.Start(ctx, rawLines)
		close(rawLines)
	}()

	// Stage 2: Ingest pipeline classifies raw lines -> events
	go func() {
		pipe.Run(ctx, rawLines, events)
		close(events)
	}()

	// Stage 3: Fan-in events to bus
	go func() {
		for evt := range events {
			bus.Publish(evt)
		}
	}()

	// Start WebSocket client (blocks until shutdown)
	wsClient.Start(ctx)
}
