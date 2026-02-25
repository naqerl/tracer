package app

import (
	"context"
	"errors"
	"testing"
	"time"

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
	a := New(src, stubParser{}, "demo-app")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
