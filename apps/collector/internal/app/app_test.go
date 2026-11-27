package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tracer/apps/collector/internal/dropstats"
	"tracer/apps/collector/internal/lifecycle"
	"tracer/libs/types"
)

type stubSource struct {
	openErr error
	lines   []string
	nextErr error
	idx     int
}

func (s *stubSource) Open(context.Context) error { return s.openErr }

func (s *stubSource) Next(context.Context) (string, error) {
	if s.idx < len(s.lines) {
		line := s.lines[s.idx]
		s.idx++
		return line, nil
	}
	if s.nextErr != nil {
		return "", s.nextErr
	}
	return "", context.Canceled
}

func (s *stubSource) Close() error { return nil }

type stubParser struct{}

func (stubParser) Parse(line string) (types.LogEntry, error) {
	return types.LogEntry{
		Timestamp: time.Now().UTC(),
		Level:     types.LevelInfo,
		Message:   line,
	}, nil
}

// blockingSource yields its lines, then blocks until the context ends so a
// test can observe the producer after the last line was processed.
type blockingSource struct {
	lines     []string
	idx       int
	exhausted chan struct{}
	once      sync.Once
}

func (s *blockingSource) Open(context.Context) error { return nil }

func (s *blockingSource) Next(ctx context.Context) (string, error) {
	if s.idx < len(s.lines) {
		line := s.lines[s.idx]
		s.idx++
		return line, nil
	}
	s.once.Do(func() { close(s.exhausted) })
	<-ctx.Done()
	return "", ctx.Err()
}

func (s *blockingSource) Close() error { return nil }

func TestProduce_EmitsLogAndSourceReadError(t *testing.T) {
	src := &stubSource{
		lines:   []string{"ok"},
		nextErr: errors.New("boom"),
	}
	a := New(src, stubParser{}, lifecycle.Detector{}, "demo-app", nil)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	stream := a.Produce(ctx)

	var (
		hasLog     bool
		hasReadErr bool
	)

	for evt := range stream {
		switch e := evt.(type) {
		case types.LogEvent:
			hasLog = hasLog || e.App == "demo-app" && e.Entry.Message == "ok"
		case types.ErrorEvent:
			hasReadErr = hasReadErr || (e.App == "demo-app" && e.ErrType == "source_read_failure")
		}
		if hasLog && hasReadErr {
			cancel()
		}
	}

	if !hasLog {
		t.Fatalf("expected log event")
	}
	if !hasReadErr {
		t.Fatalf("expected source_read_failure error event")
	}
}

// alwaysFailParser never parses; used to verify parse failures are dropped.
type alwaysFailParser struct{}

func (alwaysFailParser) Parse(string) (types.LogEntry, error) {
	return types.LogEntry{}, errors.New("parse failed: invalid character 'ð' looking for beginning of value")
}

// TestProduce_ParseErrorIsDropped verifies that unparseable lines are counted
// without emitting a log event or a record per line, and that one interval
// report summarizes the whole batch.
func TestProduce_ParseErrorIsDropped(t *testing.T) {
	const n = 5

	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("not-json-%d", i)
	}

	src := &blockingSource{lines: lines, exhausted: make(chan struct{})}
	var buf bytes.Buffer
	drops := dropstats.New(slog.New(slog.NewTextHandler(&buf, nil)))
	a := New(src, alwaysFailParser{}, lifecycle.Detector{}, "demo-app", drops)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	stream := a.Produce(ctx)

	var (
		logEvents  int32
		parseError int32
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for evt := range stream {
			switch e := evt.(type) {
			case types.LogEvent:
				atomic.AddInt32(&logEvents, 1)
			case types.ErrorEvent:
				if e.ErrType == "parse_error" {
					atomic.AddInt32(&parseError, 1)
				}
			}
		}
	}()

	select {
	case <-src.exhausted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the source to be drained")
	}

	if got := buf.String(); got != "" {
		t.Fatalf("expected no log records while producing, got %q", got)
	}
	if got := atomic.LoadInt32(&logEvents); got != 0 {
		t.Fatalf("expected no log events, got %d", got)
	}

	report := drops.Report()
	stats, ok := report["demo-app"]
	if !ok {
		t.Fatalf("expected a drop report for demo-app, got %+v", report)
	}
	if stats.DroppedLines != n {
		t.Fatalf("dropped lines = %d, want %d", stats.DroppedLines, n)
	}
	if stats.LastSample != lines[n-1] {
		t.Fatalf("last sample = %q, want %q", stats.LastSample, lines[n-1])
	}

	summary := buf.String()
	if got := strings.Count(summary, "dropped unparseable log lines"); got != 1 {
		t.Fatalf("expected exactly one summary, got %d in %q", got, summary)
	}
	if strings.Contains(summary, "could not parse log line") {
		t.Fatalf("old drop message must not appear: %q", summary)
	}
	if !strings.Contains(summary, fmt.Sprintf("count=%d", n)) {
		t.Fatalf("summary missing count %d: %q", n, summary)
	}
	if !strings.Contains(summary, fmt.Sprintf("total=%d", n)) {
		t.Fatalf("summary missing total %d: %q", n, summary)
	}

	cancel()
	<-done
	if got := atomic.LoadInt32(&parseError); got != 0 {
		t.Fatalf("expected no parse_error events, got %d", got)
	}
}

// TestProduce_EOFIsSilentRetry verifies that a clean end of stream (io.EOF)
// is not reported as a source_read_failure: the producer should retry quietly
// instead of spamming error events.
func TestProduce_EOFIsSilentRetry(t *testing.T) {
	src := &stubSource{nextErr: io.EOF}
	a := New(src, stubParser{}, lifecycle.Detector{}, "demo-app", nil)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	stream := a.Produce(ctx)

	for evt := range stream {
		switch e := evt.(type) {
		case types.ErrorEvent:
			t.Fatalf("expected no error event for io.EOF, got %s: %s", e.ErrType, e.Message)
		default:
			t.Fatalf("unexpected event for io.EOF: %T", evt)
		}
	}
}

type failParser struct{}

func (failParser) Parse(string) (types.LogEntry, error) {
	return types.LogEntry{}, errors.New("must not parse lifecycle line")
}

func TestProduce_EmitsLifecycleEventBeforeParsing(t *testing.T) {
	src := &stubSource{
		lines: []string{"service has started"},
	}
	var detector lifecycle.Detector
	if err := detector.RegisterPattern(types.LifecycleStart, "started"); err != nil {
		t.Fatalf("detector setup failed: %v", err)
	}
	a := New(src, failParser{}, detector, "demo-app", nil)

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	stream := a.Produce(ctx)

	for evt := range stream {
		lifecycle, ok := evt.(types.LifecycleEvent)
		if !ok {
			continue
		}
		if lifecycle.App != "demo-app" {
			t.Fatalf("unexpected app: %s", lifecycle.App)
		}
		if lifecycle.Kind != types.LifecycleStart {
			t.Fatalf("unexpected kind: %s", lifecycle.Kind)
		}
		if lifecycle.RawLine != "service has started" {
			t.Fatalf("unexpected raw line: %q", lifecycle.RawLine)
		}
		cancel()
		return
	}

	t.Fatalf("expected lifecycle event")
}
