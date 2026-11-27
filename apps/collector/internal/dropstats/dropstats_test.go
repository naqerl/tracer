package dropstats

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestTrackerReportCountsAndResetsWindow(t *testing.T) {
	drops := New(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))

	drops.Record("alpha", "first")
	drops.Record("alpha", "second")
	drops.Record("beta", "third")

	report := drops.Report()
	if len(report) != 2 {
		t.Fatalf("expected 2 apps in report, got %d", len(report))
	}
	if got := report["alpha"].DroppedLines; got != 2 {
		t.Fatalf("alpha dropped lines = %d, want 2", got)
	}
	if got := report["alpha"].LastSample; got != "second" {
		t.Fatalf("alpha last sample = %q, want second", got)
	}
	if report["alpha"].UpdatedAt.IsZero() {
		t.Fatal("alpha updated at must be set")
	}
	if got := report["beta"].DroppedLines; got != 1 {
		t.Fatalf("beta dropped lines = %d, want 1", got)
	}

	if second := drops.Report(); second != nil {
		t.Fatalf("expected no report after reset, got %+v", second)
	}

	drops.Record("alpha", "third")
	report = drops.Report()
	if got := report["alpha"].DroppedLines; got != 1 {
		t.Fatalf("alpha dropped lines after reset = %d, want 1", got)
	}
}

func TestTrackerReportWarnsOncePerApp(t *testing.T) {
	var buf bytes.Buffer
	drops := New(slog.New(slog.NewTextHandler(&buf, nil)))

	drops.Record("alpha", "bad line")
	drops.Record("alpha", "worse line")
	drops.Record("beta", "other bad line")

	drops.Report()

	logs := buf.String()
	if got := strings.Count(logs, "dropped unparseable log lines"); got != 2 {
		t.Fatalf("expected one summary per app, got %d in %q", got, logs)
	}
	if !strings.Contains(logs, "app=alpha") || !strings.Contains(logs, "count=2") || !strings.Contains(logs, "total=2") {
		t.Fatalf("alpha summary missing count or total: %q", logs)
	}
	if !strings.Contains(logs, "sample=\"worse line\"") {
		t.Fatalf("alpha summary missing last sample: %q", logs)
	}
	if strings.Contains(logs, "could not parse log line") {
		t.Fatalf("old drop message must not appear: %q", logs)
	}

	before := buf.Len()
	drops.Report()
	if buf.Len() != before {
		t.Fatalf("empty interval must not log again: %q", buf.String()[before:])
	}
}

func TestTrackerSelfAppsKeepCountsWithoutSummary(t *testing.T) {
	var buf bytes.Buffer
	drops := New(slog.New(slog.NewTextHandler(&buf, nil)))
	drops.SetSelfApps([]string{"tracer-collector"})

	drops.Record("tracer-collector", "self line")
	drops.Record("other-app", "other line")

	report := drops.Report()
	if got := report["tracer-collector"].DroppedLines; got != 1 {
		t.Fatalf("self dropped lines = %d, want 1", got)
	}
	if got := report["other-app"].DroppedLines; got != 1 {
		t.Fatalf("other dropped lines = %d, want 1", got)
	}

	logs := buf.String()
	if strings.Contains(logs, "app=tracer-collector") {
		t.Fatalf("self app summary must be suppressed: %q", logs)
	}
	if !strings.Contains(logs, "app=other-app") {
		t.Fatalf("non-self app summary missing: %q", logs)
	}
}

func TestTrackerTruncatesLastSample(t *testing.T) {
	drops := New(nil)
	line := strings.Repeat("x", 500)

	drops.Record("alpha", line)

	report := drops.Report()
	sample := report["alpha"].LastSample
	if len(sample) != 200 {
		t.Fatalf("sample length = %d, want 200", len(sample))
	}
	if !strings.HasSuffix(sample, "...") {
		t.Fatalf("sample should end with ellipsis, got %q", sample)
	}
}

func TestNilTrackerIsNoop(t *testing.T) {
	var drops *Tracker
	drops.Record("alpha", "line")
	drops.SetSelfApps([]string{"alpha"})
	if got := drops.Report(); got != nil {
		t.Fatalf("nil tracker report = %+v, want nil", got)
	}
}
