package app

import (
	"context"
	"time"

	"tracer/apps/collector/internal/lifecycle"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/libs/types"
)

type App struct {
	Name string

	source    source.Source
	parser    parser.Parser
	lifecycle lifecycle.Detector
}

func New(s source.Source, p parser.Parser, lifecycle lifecycle.Detector, name string) App {
	return App{
		Name: name,

		source:    s,
		parser:    p,
		lifecycle: lifecycle,
	}
}

func (a App) Produce(ctx context.Context) <-chan types.Event {
	out := make(chan types.Event, 100)
	go func() {
		defer close(out)
		backoff := 250 * time.Millisecond
		emit := func(evt types.Event) bool {
			select {
			case out <- evt:
				return true
			case <-ctx.Done():
				return false
			}
		}

		for {
			if ctx.Err() != nil {
				return
			}

			if err := a.source.Open(ctx); err != nil {
				if !emit(types.ErrorEvent{
					Time:    time.Now().UTC(),
					App:     a.Name,
					Source:  "source",
					ErrType: "source_open_failure",
					Message: err.Error(),
				}) {
					return
				}
			} else {
				for {
					line, err := a.source.Next(ctx)
					if err != nil {
						_ = a.source.Close()
						if ctx.Err() != nil {
							return
						}
						if !emit(types.ErrorEvent{
							Time:    time.Now().UTC(),
							App:     a.Name,
							Source:  "source",
							ErrType: "source_read_failure",
							Message: err.Error(),
						}) {
							return
						}
						break
					}
					if kind, ok := a.lifecycle.Detect(line); ok {
						if !emit(types.LifecycleEvent{
							Time:    time.Now().UTC(),
							App:     a.Name,
							Kind:    kind,
							RawLine: line,
						}) {
							return
						}
						continue
					}
					entry, err := a.parser.Parse(line)
					if err == nil {
						if !emit(types.LogEvent{
							Time:  entry.Timestamp,
							App:   a.Name,
							Entry: entry,
						}) {
							return
						}
					} else {
						if !emit(types.ErrorEvent{
							Time:    time.Now().UTC(),
							App:     a.Name,
							Source:  "parser",
							ErrType: "parse_error",
							Message: err.Error(),
						}) {
							return
						}
					}
				}
			}

			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < 5*time.Second {
				backoff *= 2
				if backoff > 5*time.Second {
					backoff = 5 * time.Second
				}
			}
		}
	}()
	return out
}
