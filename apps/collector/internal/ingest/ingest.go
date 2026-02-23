package ingest

import (
	"context"

	"github.com/ai-shift/tracer/apps/collector/internal/classify"
	"github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/events"
	"github.com/ai-shift/tracer/libs/types"
)

// Pipeline fans out raw lines to classifiers for each app.
type Pipeline struct {
	classifiers map[string]*classify.Classifier
}

// New creates a pipeline from app configurations.
func New(apps []config.ResolvedApp) (*Pipeline, error) {
	classifiers := make(map[string]*classify.Classifier, len(apps))

	for _, app := range apps {
		c, err := classify.New(app)
		if err != nil {
			return nil, err
		}
		classifiers[app.Name] = c
	}

	return &Pipeline{classifiers: classifiers}, nil
}

// Run processes raw lines and emits events to the output channel.
func (p *Pipeline) Run(ctx context.Context, input <-chan types.RawLine, output chan<- types.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case rl, ok := <-input:
			if !ok {
				return
			}
			if evt := p.process(rl); evt != nil {
				select {
				case output <- evt:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// RunWithBus processes raw lines and publishes directly to event bus.
func (p *Pipeline) RunWithBus(ctx context.Context, input <-chan types.RawLine, bus *events.Bus) {
	for {
		select {
		case <-ctx.Done():
			return
		case rl, ok := <-input:
			if !ok {
				return
			}
			if evt := p.process(rl); evt != nil {
				bus.Publish(evt)
			}
		}
	}
}

func (p *Pipeline) process(rl types.RawLine) types.Event {
	classifier, ok := p.classifiers[rl.App]
	if !ok {
		return nil
	}
	return classifier.Process(rl)
}
