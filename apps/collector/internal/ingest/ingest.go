package ingest

import (
	"context"
	"log/slog"

	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/processor"
	"github.com/ai-shift/tracer/libs/types"
)

// Pipeline fans out raw lines to processors for each app.
type Pipeline struct {
	processors map[string]*processor.Processor
}

// New creates a pipeline from app configurations.
func New(apps []config.ResolvedApp) (*Pipeline, error) {
	processors := make(map[string]*processor.Processor, len(apps))

	for _, app := range apps {
		p, err := processor.New(app)
		if err != nil {
			return nil, err
		}
		processors[app.Name] = p
	}

	return &Pipeline{processors: processors}, nil
}

// Run processes raw lines and emits events to the output channel.
func (p *Pipeline) Run(ctx context.Context, input <-chan types.RawLine, output chan<- types.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case rl, ok := <-input:
			if !ok {
				slog.Info("input channel closed")
				return
			}
			proc, ok := p.processors[rl.App]
			if !ok {
				slog.Error("processor not found", "app", rl.App)
				continue
			}
			evt := proc.Process(rl)
			select {
			case output <- evt:
			case <-ctx.Done():
				return
			}
		}
	}
}
