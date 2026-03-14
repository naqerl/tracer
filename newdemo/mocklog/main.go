package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type contextKey string

const (
	traceIDKey contextKey = "trace_id"
	flowIDKey  contextKey = "flow_id"
)

type config struct {
	service        string
	component      string
	format         string
	traceMode      string
	burstRequests  int
	steadyInterval time.Duration
	nestedDepth    int
	warnEvery      int
	errorEvery     int
}

var reqSeq atomic.Uint64

func main() {
	cfg := parseFlags()
	logger := newLogger(cfg)

	logger.Info("mock log generator starting",
		"service", cfg.service,
		"component", cfg.component,
		"format", cfg.format,
		"trace_mode", cfg.traceMode,
		"burst_requests", cfg.burstRequests,
		"steady_interval", cfg.steadyInterval.String(),
	)

	runBurst(logger, cfg, cfg.burstRequests)

	ticker := time.NewTicker(cfg.steadyInterval)
	defer ticker.Stop()

	for range ticker.C {
		runBurst(logger, cfg, 1)
	}
}

func parseFlags() config {
	cfg := config{}

	flag.StringVar(&cfg.service, "service", "demo-app", "logical service name")
	flag.StringVar(&cfg.component, "component", "app", "component label")
	flag.StringVar(&cfg.format, "format", "json", "log format: json or text")
	flag.StringVar(&cfg.traceMode, "trace-mode", "always", "trace behavior: always, never, mixed")
	flag.IntVar(&cfg.burstRequests, "burst-requests", 18, "number of requests emitted on startup")
	flag.DurationVar(&cfg.steadyInterval, "steady-interval", 5*time.Minute, "interval between steady-state bursts")
	flag.IntVar(&cfg.nestedDepth, "nested-depth", 4, "number of nested steps per request")
	flag.IntVar(&cfg.warnEvery, "warn-every", 3, "emit a warning on every Nth request")
	flag.IntVar(&cfg.errorEvery, "error-every", 7, "emit an error on every Nth request")
	flag.Parse()

	cfg.format = strings.ToLower(strings.TrimSpace(cfg.format))
	cfg.traceMode = strings.ToLower(strings.TrimSpace(cfg.traceMode))

	if cfg.format != "json" && cfg.format != "text" {
		fail("unsupported -format value: %s", cfg.format)
	}
	if cfg.traceMode != "always" && cfg.traceMode != "never" && cfg.traceMode != "mixed" {
		fail("unsupported -trace-mode value: %s", cfg.traceMode)
	}
	if cfg.burstRequests < 1 {
		fail("-burst-requests must be >= 1")
	}
	if cfg.nestedDepth < 2 {
		fail("-nested-depth must be >= 2")
	}
	if cfg.warnEvery < 1 {
		fail("-warn-every must be >= 1")
	}
	if cfg.errorEvery < 1 {
		fail("-error-every must be >= 1")
	}

	return cfg
}

func newLogger(cfg config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	var handler slog.Handler
	if cfg.format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

func runBurst(logger *slog.Logger, cfg config, count int) {
	for i := 0; i < count; i++ {
		idx := int(reqSeq.Add(1))
		ctx := context.Background()
		flowID := fmt.Sprintf("%s-%06d", slug(cfg.service), idx)
		ctx = context.WithValue(ctx, flowIDKey, flowID)
		if traceID := traceIDFor(cfg.traceMode, idx); traceID != "" {
			ctx = context.WithValue(ctx, traceIDKey, traceID)
		}

		flowKind := "http"
		if strings.Contains(cfg.component, "worker") {
			flowKind = "job"
		}
		if strings.Contains(cfg.component, "cron") {
			flowKind = "schedule"
		}

		logEvent(ctx, logger, slog.LevelInfo, "flow.started",
			"service", cfg.service,
			"component", cfg.component,
			"flow_kind", flowKind,
			"burst", count > 1,
		)
		walkFlow(ctx, logger, cfg, idx, flowKind, 1)
		logEvent(ctx, logger, slog.LevelInfo, "flow.completed",
			"service", cfg.service,
			"component", cfg.component,
			"result", resultFor(idx),
		)

		if count > 1 {
			time.Sleep(75 * time.Millisecond)
		}
	}
}

func walkFlow(ctx context.Context, logger *slog.Logger, cfg config, idx int, flowKind string, depth int) {
	stepName := stepName(flowKind, depth)
	logEvent(ctx, logger, slog.LevelDebug, "step.enter",
		"service", cfg.service,
		"component", cfg.component,
		"step", stepName,
		"depth", depth,
	)

	if depth == cfg.nestedDepth {
		logEvent(ctx, logger, slog.LevelInfo, "step.leaf",
			"service", cfg.service,
			"component", cfg.component,
			"step", stepName,
			"latency_ms", 20+(idx%17)*5,
		)
	} else {
		walkFlow(ctx, logger, cfg, idx, flowKind, depth+1)
	}

	if idx%cfg.errorEvery == 0 && depth == cfg.nestedDepth-1 {
		logEvent(ctx, logger, slog.LevelError, "dependency.failed",
			"service", cfg.service,
			"component", cfg.component,
			"step", stepName,
			"target", dependencyName(flowKind),
			"retryable", idx%2 == 0,
		)
	} else if idx%cfg.warnEvery == 0 && depth == cfg.nestedDepth-2 {
		logEvent(ctx, logger, slog.LevelWarn, "dependency.slow",
			"service", cfg.service,
			"component", cfg.component,
			"step", stepName,
			"target", dependencyName(flowKind),
			"latency_ms", 120+idx%90,
		)
	}

	logEvent(ctx, logger, slog.LevelDebug, "step.exit",
		"service", cfg.service,
		"component", cfg.component,
		"step", stepName,
		"depth", depth,
	)
}

func logEvent(ctx context.Context, logger *slog.Logger, level slog.Level, message string, kv ...any) {
	attrs := make([]any, 0, len(kv)+4)
	attrs = append(attrs, "ts_group", time.Now().UTC().Format(time.RFC3339))
	if traceID, ok := ctx.Value(traceIDKey).(string); ok && traceID != "" {
		attrs = append(attrs, "trace_id", traceID)
	}
	if flowID, ok := ctx.Value(flowIDKey).(string); ok && flowID != "" {
		attrs = append(attrs, "flow_id", flowID)
	}
	attrs = append(attrs, kv...)
	logger.Log(ctx, level, message, attrs...)
}

func traceIDFor(mode string, idx int) string {
	switch mode {
	case "always":
		return fmt.Sprintf("trace-%08x", idx*97)
	case "mixed":
		if idx%2 == 0 {
			return fmt.Sprintf("trace-%08x", idx*97)
		}
		return ""
	default:
		return ""
	}
}

func stepName(flowKind string, depth int) string {
	switch flowKind {
	case "job":
		steps := []string{"job.dequeue", "job.decode", "job.enrich", "job.dispatch", "job.flush"}
		if depth-1 < len(steps) {
			return steps[depth-1]
		}
	case "schedule":
		steps := []string{"tick.start", "tick.load", "tick.aggregate", "tick.store", "tick.emit"}
		if depth-1 < len(steps) {
			return steps[depth-1]
		}
	default:
		steps := []string{"request.accept", "auth.resolve", "inventory.lookup", "db.commit", "response.write"}
		if depth-1 < len(steps) {
			return steps[depth-1]
		}
	}
	return fmt.Sprintf("%s.step.%d", flowKind, depth)
}

func dependencyName(flowKind string) string {
	switch flowKind {
	case "job":
		return "queue"
	case "schedule":
		return "warehouse"
	default:
		return "postgres"
	}
}

func resultFor(idx int) string {
	switch {
	case idx%7 == 0:
		return "degraded"
	case idx%3 == 0:
		return "slow"
	default:
		return "ok"
	}
}

func slug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	return strings.ReplaceAll(s, "_", "-")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}
