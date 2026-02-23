package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/events"
	logparser "github.com/ai-shift/tracer/apps/collector/internal/parsing"
	"github.com/ai-shift/tracer/apps/collector/internal/pipeline"
	sysstats "github.com/ai-shift/tracer/apps/collector/internal/stats"
	"github.com/ai-shift/tracer/apps/collector/internal/supervisor"
	wsclient "github.com/ai-shift/tracer/apps/collector/internal/transport"
	watchdog "github.com/ai-shift/tracer/apps/collector/internal/watch"
	"github.com/ai-shift/tracer/libs/logx"
	"github.com/ai-shift/tracer/libs/types"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	// Setup structured logging
	logx.SetDefault()

	// Load configuration
	cfg, err := collectorconfig.Load(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	panelEndpoint, err := normalizePanelEndpoint(cfg.PanelEndpoint)
	if err != nil {
		slog.Error("Invalid panel endpoint", "error", err)
		os.Exit(1)
	}

	// Determine collector name: use config name, or fall back to local IP
	collectorName := cfg.Name
	if collectorName == "" {
		collectorName = getOutboundIP()
	}

	slog.Info("Starting collector",
		"name", collectorName,
		"panel_endpoint", panelEndpoint,
		"apps", len(cfg.Apps),
	)

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		slog.Info("Received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Create shared event bus
	bus := events.NewBus(1000)

	// Build sources for watchdog (just I/O, no parsing)
	sources := make([]watchdog.Source, len(cfg.Apps))
	for i, app := range cfg.Apps {
		switch app.SourceKind {
		case "file":
			sources[i] = &watchdog.FileSource{
				App:  app.Name,
				Path: app.Path,
				From: app.ReadFrom,
			}
		case "journalctl":
			sources[i] = &watchdog.JournalctlSource{
				App:   app.Name,
				Unit:  app.Unit,
				Since: app.Since,
			}
		default:
			slog.Error("Unknown source kind", "app", app.Name, "kind", app.SourceKind)
			os.Exit(1)
		}
	}

	// Build pipeline configs (for parsing)
	pipelineConfigs := make([]logparser.Config, len(cfg.Apps))
	for i, app := range cfg.Apps {
		pipelineConfigs[i] = logparser.Config{
			ResolvedApp:     app,
			ShutdownPattern: "", // Could add to config if needed
		}
	}

	// Initialize watchdog (dumb I/O)
	watcher := watchdog.New(sources)

	// Initialize pipeline (parsing stage)
	pipe, err := pipeline.New(pipelineConfigs)
	if err != nil {
		slog.Error("Failed to create pipeline", "error", err)
		os.Exit(1)
	}

	// Initialize WebSocket client
	wsClient := wsclient.New(panelEndpoint, cfg.APIKey, collectorName, pipe.AppNames(), bus)

	// Create channels for pipeline stages
	rawLines := make(chan types.RawLine, 100)
	events := make(chan types.Event, 100)

	// Start stats producer if interval is set
	if cfg.StatsInterval != "" {
		interval, err := time.ParseDuration(cfg.StatsInterval)
		if err != nil {
			slog.Warn("Invalid stats_interval, disabling stats", "error", err)
		} else if interval > 0 {
			dataDir := cfg.DataDir
			if dataDir == "" {
				dataDir = "/"
			}
			statsCollector := sysstats.New(dataDir)
			statsProducer := sysstats.NewProducer(statsCollector, interval, bus)
			go statsProducer.Start(ctx)
			slog.Info("System stats enabled", "interval", interval, "data_dir", dataDir)
		}
	}

	// Start supervisor to monitor sources and emit error events
	sourcesForSupervisor := make([]supervisor.Source, len(sources))
	for i, s := range sources {
		sourcesForSupervisor[i] = s.(supervisor.Source)
	}
	super := supervisor.New(sourcesForSupervisor, bus)
	go super.Run(ctx)

	// Start pipeline stages
	// Stage 1: Watchdog reads sources -> raw lines
	go func() {
		watcher.Start(ctx, rawLines)
		close(rawLines)
	}()

	// Stage 2: Pipeline parses raw lines -> events
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

func getOutboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		hostname, _ := os.Hostname()
		if hostname != "" {
			return hostname
		}
		return "unknown"
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP.String()
}

func normalizePanelEndpoint(endpoint string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}

	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported scheme: %s", parsed.Scheme)
	}

	cleanPath := strings.TrimSuffix(parsed.Path, "/")
	if cleanPath == "" {
		parsed.Path = "/ingest/ws/ingest"
	} else if cleanPath == "/ingest/ws/ingest" {
		parsed.Path = cleanPath
	} else {
		parsed.Path = cleanPath + "/ingest/ws/ingest"
	}

	return parsed.String(), nil
}
