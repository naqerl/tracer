package app

import (
	"context"

	"github.com/ai-shift/tracer/apps/collector/internal/parser"
	"github.com/ai-shift/tracer/apps/collector/internal/source"
	"github.com/ai-shift/tracer/libs/types"
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
		lines := a.source.Lines(ctx)

		for {
			select {
			case <-ctx.Done():
				return
			case line, ok := <-lines:
				if !ok {
					return
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
						Message: err.Error(),
					}
				}
			}
		}
	}()
	return out
}
