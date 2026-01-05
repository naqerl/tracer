package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ai-shift/tracer/config"
	"github.com/ai-shift/tracer/core/services/filewatcher"
	"github.com/ai-shift/tracer/core/services/wsclient"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	// Setup structured logging
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Load configuration
	cfg, err := config.LoadCollector(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("Starting collector",
		"app_name", cfg.AppName,
		"panel_endpoint", cfg.PanelEndpoint,
		"log_files", len(cfg.LogFiles),
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

	// Initialize file watcher
	watcher, err := filewatcher.New(cfg.LogFiles)
	if err != nil {
		slog.Error("Failed to create file watcher", "error", err)
		os.Exit(1)
	}

	// Initialize WebSocket client
	wsClient := wsclient.New(cfg.PanelEndpoint, cfg.AppName, watcher.LogChan)

	// Start services
	go watcher.Start(ctx)
	wsClient.Start(ctx)
}
