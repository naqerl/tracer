package watchdog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
)

func TestWatcherReadFromEndSkipsExistingLines(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "app.log")
	existing := jsonLine("2026-02-23T10:00:00Z", "existing")
	if err := os.WriteFile(logPath, []byte(existing+"\n"), 0o644); err != nil {
		t.Fatalf("write existing line: %v", err)
	}

	cfg := collectorconfig.ResolvedApp{
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
	}

	svc, err := New([]collectorconfig.ResolvedApp{cfg})
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

	got := awaitLog(t, svc.LogChan, 3*time.Second)
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

	cfg := collectorconfig.ResolvedApp{
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
	}

	svc, err := New([]collectorconfig.ResolvedApp{cfg})
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Start(ctx)

	got := awaitLog(t, svc.LogChan, 3*time.Second)
	if got.Entry.Message != "existing" {
		t.Fatalf("expected existing line to be read first, got message=%q", got.Entry.Message)
	}
}

func TestParseLineJournalFallbackToMessage(t *testing.T) {
	t.Parallel()

	app := collectorconfig.ResolvedApp{
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
	}

	svc, err := New([]collectorconfig.ResolvedApp{app})
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	if len(svc.sources) != 1 {
		t.Fatalf("expected one source")
	}

	line := `{"__REALTIME_TIMESTAMP":"1708683072000000","PRIORITY":"6","MESSAGE":"2026-02-23T10:11:12Z INFO trace=trace-77 app started"}`
	entry, err := svc.parseLine(svc.sources[0], line)
	if err != nil {
		t.Fatalf("parseLine failed: %v", err)
	}

	if entry.Message != "2026-02-23T10:11:12Z INFO trace=trace-77 app started" {
		t.Fatalf("unexpected message: %q", entry.Message)
	}
	if entry.TraceID != "trace-77" {
		t.Fatalf("unexpected trace id: %q", entry.TraceID)
	}
}

func awaitLog(t *testing.T, ch <-chan AppLogEntry, timeout time.Duration) AppLogEntry {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for log entry")
	}
	return AppLogEntry{}
}

func jsonLine(ts, msg string) string {
	return fmt.Sprintf(`{"timestamp":"%s","level":"info","trace_id":"trace-x","message":"%s"}`, ts, msg)
}
