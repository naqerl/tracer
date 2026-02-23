package watchdog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/events"
	logparser "github.com/ai-shift/tracer/apps/collector/internal/parsing"
	"github.com/ai-shift/tracer/libs/types"
)

func TestWatcherReadFromEndSkipsExistingLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	existing := jsonLine("2026-02-23T10:00:00Z", "existing")
	if err := os.WriteFile(logPath, []byte(existing+"\n"), 0o644); err != nil {
		t.Fatalf("write existing line: %v", err)
	}

	bus := events.NewBus(10)
	cfg := logparser.Config{
		ResolvedApp: collectorconfig.ResolvedApp{
			Name:            "app",
			SourceKind:      "file",
			Path:            logPath,
			ReadFrom:        "end",
			Source:          logPath,
			ParserKind:      "json",
			TimestampField:  "timestamp",
			TimestampFormat: time.RFC3339,
			LevelField:      "level",
			TraceIDField:    "trace_id",
			MessageField:    "message",
		},
	}

	svc, err := New([]logparser.Config{cfg}, bus)
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Start(ctx)

	// Give watcher time to open file and seek to end.
	time.Sleep(200 * time.Millisecond)

	newLine := jsonLine("2026-02-23T10:00:01Z", "new")
	f, err := os.OpenFile(logPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open log for append: %v", err)
	}
	if _, err := f.WriteString(newLine + "\n"); err != nil {
		t.Fatalf("append line: %v", err)
	}
	_ = f.Close()

	ch := bus.Subscribe()
	got := awaitLog(t, ch, 3*time.Second)
	if got.Entry.Message != "new" {
		t.Fatalf("expected only newly appended line, got message=%q", got.Entry.Message)
	}
}

func TestWatcherReadFromStartReadsExistingLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	existing := jsonLine("2026-02-23T10:00:00Z", "existing")
	if err := os.WriteFile(logPath, []byte(existing+"\n"), 0o644); err != nil {
		t.Fatalf("write existing line: %v", err)
	}

	bus := events.NewBus(10)
	cfg := logparser.Config{
		ResolvedApp: collectorconfig.ResolvedApp{
			Name:            "app",
			SourceKind:      "file",
			Path:            logPath,
			ReadFrom:        "start",
			Source:          logPath,
			ParserKind:      "json",
			TimestampField:  "timestamp",
			TimestampFormat: time.RFC3339,
			LevelField:      "level",
			TraceIDField:    "trace_id",
			MessageField:    "message",
		},
	}

	svc, err := New([]logparser.Config{cfg}, bus)
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Start(ctx)

	ch := bus.Subscribe()
	got := awaitLog(t, ch, 3*time.Second)
	if got.Entry.Message != "existing" {
		t.Fatalf("expected existing line to be read first, got message=%q", got.Entry.Message)
	}
}

func TestPipelineJournalctlExtraction(t *testing.T) {
	t.Parallel()

	cfg := logparser.Config{
		ResolvedApp: collectorconfig.ResolvedApp{
			Name:            "svc",
			SourceKind:      "journalctl",
			Source:          "journalctl:svc.service",
			Unit:            "svc.service",
			Since:           "now",
			ParserKind:      "text",
			TimestampRegex:  `^(\S+)`,
			TimestampFormat: time.RFC3339,
			LevelRegex:      `\b(INFO|WARN|ERROR|DEBUG)\b`,
			TraceIDRegex:    `trace=([^\s]+)`,
		},
	}

	pipeline, err := logparser.NewPipeline(cfg)
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}

	line := `{"__REALTIME_TIMESTAMP":"1708683072000000","PRIORITY":"6","MESSAGE":"2026-02-23T10:11:12Z INFO trace=trace-77 app started"}`
	rl := types.RawLine{App: "svc", Line: line}
	evt := pipeline.Process(rl)

	logEvt, ok := evt.(types.LogEvent)
	if !ok {
		t.Fatalf("expected LogEvent, got %T", evt)
	}

	if logEvt.Entry.Message != "2026-02-23T10:11:12Z INFO trace=trace-77 app started" {
		t.Fatalf("unexpected message: %q", logEvt.Entry.Message)
	}
	if logEvt.Entry.TraceID != "trace-77" {
		t.Fatalf("unexpected trace id: %q", logEvt.Entry.TraceID)
	}
}

func TestPipelineStartupDetection(t *testing.T) {
	t.Parallel()

	cfg := logparser.Config{
		ResolvedApp: collectorconfig.ResolvedApp{
			Name:            "app",
			SourceKind:      "file",
			Path:            "/tmp/app.log",
			Source:          "/tmp/app.log",
			ParserKind:      "json",
			TimestampField:  "timestamp",
			TimestampFormat: time.RFC3339,
			LevelField:      "level",
			MessageField:    "message",
			StartupPattern:  "Server started",
		},
	}

	pipeline, err := logparser.NewPipeline(cfg)
	if err != nil {
		t.Fatalf("new pipeline: %v", err)
	}

	line := `{"timestamp":"2026-02-23T10:00:00Z","level":"info","message":"Server started on port 8080"}`
	rl := types.RawLine{App: "app", Line: line}
	evt := pipeline.Process(rl)

	startupEvt, ok := evt.(types.StartupEvent)
	if !ok {
		t.Fatalf("expected StartupEvent, got %T", evt)
	}

	if startupEvt.App != "app" {
		t.Fatalf("unexpected app: %q", startupEvt.App)
	}
	if startupEvt.Message != "Server started on port 8080" {
		t.Fatalf("unexpected message: %q", startupEvt.Message)
	}
}

func awaitLog(t *testing.T, ch <-chan types.Event, timeout time.Duration) types.LogEvent {
	t.Helper()
	select {
	case got := <-ch:
		logEvt, ok := got.(types.LogEvent)
		if !ok {
			t.Fatalf("expected LogEvent, got %T", got)
		}
		return logEvt
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for log entry")
	}
	return types.LogEvent{}
}

func jsonLine(ts, msg string) string {
	return fmt.Sprintf(`{"timestamp":"%s","level":"info","trace_id":"trace-x","message":"%s"}`, ts, msg)
}
