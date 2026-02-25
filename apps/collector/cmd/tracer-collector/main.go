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

	"github.com/ai-shift/tracer/apps/collector/internal/app"
	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/parser"
	"github.com/ai-shift/tracer/apps/collector/internal/source"
	"github.com/ai-shift/tracer/apps/collector/internal/stats"
	"github.com/ai-shift/tracer/apps/collector/internal/transport"
	"github.com/ai-shift/tracer/libs/logx"
	"github.com/ai-shift/tracer/libs/types"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	logx.SetDefault()

	cfg := config.Load(*configPath)

	slog.Info("Starting collector",
		"name", cfg.Transport.Name,
		"panelHost", cfg.Transport.PanelHost,
		"apps", len(cfg.Apps),
	)

	// Setup context with cancellation on shutdown signals
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := serve(ctx, cfg); err != nil {
		log.Fatalf("failed to serve: %s", err)
	}
}

func serve(ctx context.Context, cfg config.Config) error {
	var wg sync.WaitGroup
	mpsc := make(chan types.Event, 1000)

	// Sources
	sources := make(map[string]source.Source, len(cfg.Sources))
	for _, spec := range cfg.Sources {
		sources[spec.ID] = source.New(spec)
	}
	if len(sources) != len(cfg.Sources) {
		return fmt.Errorf("source ids should be unique")
	}

	// Parsers
	parsers := make(map[string]parser.Parser, len(cfg.Parsers))
	for _, spec := range cfg.Parsers {
		parsers[spec.ID] = parser.New(spec)
	}
	if len(parsers) != len(cfg.Parsers) {
		return fmt.Errorf("source ids should be unique")
	}

	// Create apps
	var apps []app.App
	var appNames []string
	for _, spec := range cfg.Apps {
		s, ok := sources[spec.SourceID]
		if !ok {
			return fmt.Errorf("source with id=%s not found", spec.SourceID)
		}
		p, ok := parsers[spec.ParserID]
		if !ok {
			return fmt.Errorf("parser with id=%s not found", spec.ParserID)
		}
		apps = append(apps, app.New(s, p, spec.Name))
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
			mpsc <- evt
		}
	})
	for _, app := range apps {
		wg.Go(func() {
			for evt := range app.Produce(ctx) {
				mpsc <- evt
			}
		})
	}
	wg.Wait()

	return nil
}
