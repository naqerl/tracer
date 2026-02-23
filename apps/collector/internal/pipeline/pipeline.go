package pipeline

import (
	"context"

	"github.com/ai-shift/tracer/apps/collector/internal/events"
	logparser "github.com/ai-shift/tracer/apps/collector/internal/parsing"
	"github.com/ai-shift/tracer/libs/types"
)

// Stage represents a pipeline stage that transforms input to output.
type Stage[In, Out any] func(context.Context, <-chan In, chan<- Out)

// Pipeline transforms RawLine events into typed Events.
type Pipeline struct {
	parsers map[string]*logparser.Pipeline
}

// New creates a pipeline from app configurations.
func New(configs []logparser.Config) (*Pipeline, error) {
	parsers := make(map[string]*logparser.Pipeline, len(configs))

	for _, cfg := range configs {
		parser, err := logparser.NewPipeline(cfg)
		if err != nil {
			return nil, err
		}
		parsers[cfg.Name] = parser
	}

	return &Pipeline{parsers: parsers}, nil
}

// Run processes raw lines and publishes events to the bus.
// It reads from input channel, parses each line, and writes to output channel.
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
	parser, ok := p.parsers[rl.App]
	if !ok {
		return nil
	}
	return parser.Process(rl)
}

// AppNames returns the names of all configured apps.
func (p *Pipeline) AppNames() []string {
	names := make([]string, 0, len(p.parsers))
	for name := range p.parsers {
		names = append(names, name)
	}
	return names
}
