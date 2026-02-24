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
	"github.com/ai-shift/tracer/libs/logx"
	"github.com/ai-shift/tracer/libs/types"
)

func main() {
	configPath := flag.String("config", "", "path to config file")
	flag.Parse()

	logx.SetDefault()


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

        if err := serve(ctx, cfg); err != nil {
                log.Fatalf("failed to serve: %w", err)
        }
}

func serve(ctx context.Context, cfg config.Config) error {
        var wg sync.WaitGroup
        mpsc := make(chan type.Event, 1000)
        
        // Sources
        var sources map[string]source.Source
        for _, spec := range cfg.Sources {
                source := source.New(spec.Params)
                name := fmt.Printf("source:%s", spec.ID)
                sources[spec.ID] = super.Vised(source, name)
        }
        if len(sources) != cfg.Sources {
                return fmt.Errorf("source ids should be unique")
        }

        // Parsers
        var parsers map[string]parser.Parser
        for _, spec := range cfg.Sources {
                parsers[spec.ID] = parser.New(spec.Params)
        }
        if len(parsers) != cfg.Parsers {
                return fmt.Errorf("source ids should be unique")
        }

        // Create apps
        var apps []app.App
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
        }

        // Resource monitor
        resMonitor = resources.New(cfg.Resources, bus)

        // Transport
        transport = super.Vised(transport.New(ctx, bus), "transport")

        // Run
        wg.Go(func () { transport.Start(ctx, bus) })
        wg.Go(func () {
                for stat := range.resMonitor.Produce(ctx) {
                        mpsc <- stat
                }
        })
        wg.Go(func () { app.Start(ctx, bus) })
        for _, app := range apps {
                wg.Go(func () {
                        for log := range app.Produce(ctx) {
                                mpsc <- stat
                        }
                })
        }
        wg.wait()
}
