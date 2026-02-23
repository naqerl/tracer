package watchdog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/processor"
	"github.com/ai-shift/tracer/apps/collector/internal/watchdog/source"
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

	sources := []Source{
		source.NewFile("app", logPath, "end"),
	}

	svc := New(sources)

	rawCh := make(chan types.RawLine, 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Start(ctx, rawCh)

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

	got := awaitRawLine(t, rawCh, 3*time.Second)
	if got.Line != newLine {
		t.Fatalf("expected newly appended line, got %q", got.Line)
	}
	if got.App != "app" {
		t.Fatalf("expected app=app, got %q", got.App)
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

	sources := []Source{
		source.NewFile("app", logPath, "start"),
	}

	svc := New(sources)

	rawCh := make(chan types.RawLine, 10)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Start(ctx, rawCh)

	got := awaitRawLine(t, rawCh, 3*time.Second)
	if got.Line != existing {
		t.Fatalf("expected existing line, got %q", got.Line)
	}
}

func TestPipelineJournalctlExtraction(t *testing.T) {
	t.Parallel()

	cfg := config.ResolvedApp{
		Name:            "svc",
		SourceKind:      "journalctl",
		Source:          "journalctl:svc.service",
		Unit:            "svc.service",
		Since:           "now",
		ParserKind:      "json",
		TimestampField:  "timestamp",
		TimestampFormat: time.RFC3339,
		LevelField:      "level",
		TraceIDField:    "trace_id",
		MessageField:    "message",
	}

	proc, err := processor.New(cfg)
	if err != nil {
		t.Fatalf("new processor: %v", err)
	}

	line := `{"timestamp":"2026-02-23T10:11:12Z","level":"INFO","trace_id":"trace-77","message":"app started"}`
	rl := types.RawLine{App: "svc", Line: line}
	evt := proc.Process(rl)

	logEvt, ok := evt.(types.LogEvent)
	if !ok {
		t.Fatalf("expected LogEvent, got %T", evt)
	}

	if logEvt.Entry.Message != "app started" {
		t.Fatalf("unexpected message: %q", logEvt.Entry.Message)
	}
	if logEvt.Entry.TraceID != "trace-77" {
		t.Fatalf("unexpected trace id: %q", logEvt.Entry.TraceID)
	}
}

func TestPipelineStartupDetection(t *testing.T) {
	t.Parallel()

	cfg := config.ResolvedApp{
		Name:            "app",
		SourceKind:      "file",
		Source:          "/tmp/app.log",
		Path:            "/tmp/app.log",
		ParserKind:      "json",
		TimestampField:  "timestamp",
		TimestampFormat: time.RFC3339,
		LevelField:      "level",
		MessageField:    "message",
		StartupPattern:  "Server started",
	}

	proc, err := processor.New(cfg)
	if err != nil {
		t.Fatalf("new processor: %v", err)
	}

	line := `{"timestamp":"2026-02-23T10:00:00Z","level":"info","message":"Server started on port 8080"}`
	rl := types.RawLine{App: "app", Line: line}
	evt := proc.Process(rl)

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

func awaitRawLine(t *testing.T, ch <-chan types.RawLine, timeout time.Duration) types.RawLine {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for raw line")
	}
	return types.RawLine{}
}

func jsonLine(ts, msg string) string {
	return fmt.Sprintf(`{"timestamp":"%s","level":"info","trace_id":"trace-x","message":"%s"}`, ts, msg)
}
