// TODO: Ajax requests return 303, cause they require HX-Request: true header
// Only 200 codes should be accepts, if got 301, then new host should be tried
package main

import (
	"context"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ai-shift/tracer/config"
	"github.com/ai-shift/tracer/core/services/filewatcher"
	"github.com/ai-shift/tracer/core/services/sysstats"
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

	// Determine collector name: use config name, or fall back to local IP
	collectorName := cfg.Name
	if collectorName == "" {
		collectorName = getOutboundIP()
	}

	slog.Info("Starting collector",
		"name", collectorName,
		"panel_endpoint", cfg.PanelEndpoint,
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

	// Initialize file watcher
	watcher, err := filewatcher.New(cfg.Apps)
	if err != nil {
		slog.Error("Failed to create file watcher", "error", err)
		os.Exit(1)
	}

	// Initialize WebSocket client
	wsClient := wsclient.New(cfg.PanelEndpoint, collectorName, watcher.GetAppNames(), watcher.LogChan)

	// Configure system stats if interval is set
	if cfg.StatsInterval != "" {
		interval, err := time.ParseDuration(cfg.StatsInterval)
		if err != nil {
			slog.Warn("Invalid stats_interval, disabling stats", "error", err)
		} else if interval > 0 {
			dataDir := cfg.DataDir
			if dataDir == "" {
				dataDir = "/" // Default to root for disk monitoring
			}
			statsCollector := sysstats.New(dataDir)
			wsClient.WithStats(statsCollector, interval)
			slog.Info("System stats enabled", "interval", interval, "data_dir", dataDir)
		}
	}

	// Start services
	go watcher.Start(ctx)
	wsClient.Start(ctx)
}

// getOutboundIP returns the preferred outbound IP of this machine
func getOutboundIP() string {
	// Connect to a public address to determine local IP
	// This doesn't actually establish a connection, just determines the route
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		// Fallback to hostname if we can't determine IP
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
