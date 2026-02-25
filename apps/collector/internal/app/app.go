package app

import (
	"context"
	"time"

	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
	"tracer/libs/types"
)

type App struct {
	Name string

	source source.Source
	parser parser.Parser
}

func New(s source.Source, p parser.Parser, name string) App {
	return App{
		Name: name,

		source: s,
		parser: p,
	}
}

func (a App) Produce(ctx context.Context) <-chan types.Event {
	out := make(chan types.Event, 100)
	go func() {
		defer close(out)
		backoff := 250 * time.Millisecond
		for {
			if ctx.Err() != nil {
				return
			}

			if err := a.source.Open(ctx); err != nil {
				out <- types.ErrorEvent{
					App:     a.Name,
					Source:  "source",
					ErrType: "source_open_failure",
					Message: err.Error(),
				}
			} else {
				for {
					line, err := a.source.Next(ctx)
					if err != nil {
						_ = a.source.Close()
						if ctx.Err() != nil {
							return
						}
						out <- types.ErrorEvent{
							App:     a.Name,
							Source:  "source",
							ErrType: "source_read_failure",
							Message: err.Error(),
						}
						break
					}
					entry, err := a.parser.Parse(line)
					if err == nil {
						out <- types.LogEvent{
							App:   a.Name,
							Entry: entry,
						}
					} else {
						out <- types.ErrorEvent{
							App:     a.Name,
							Source:  "parser",
							ErrType: "parse_error",
							Message: err.Error(),
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
