package app

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

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

func TestProduce_EmitsLogAndSourceReadError(t *testing.T) {
	src := &stubSource{
		lines:   []string{"ok"},
		nextErr: errors.New("boom"),
	}
	a := New(src, stubParser{}, lifecycle.Detector{}, "demo-app")

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

// TestProduce_EOFIsSilentRetry verifies that a clean end of stream (io.EOF)
// is not reported as a source_read_failure: the producer should retry quietly
// instead of spamming error events.
func TestProduce_EOFIsSilentRetry(t *testing.T) {
	src := &stubSource{nextErr: io.EOF}
	a := New(src, stubParser{}, lifecycle.Detector{}, "demo-app")

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
	a := New(src, failParser{}, detector, "demo-app")

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
