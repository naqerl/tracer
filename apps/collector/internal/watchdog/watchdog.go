// Package watchdog manages log sources and emits raw lines.
package watchdog

import (
	"context"
	"sync"

	"github.com/ai-shift/tracer/apps/collector/internal/source"
	"github.com/ai-shift/tracer/libs/types"
)

// Source is an alias for source.Source for convenience.
type Source = source.Source

// Service manages multiple log sources.
type Service struct {
	sources []Source
}

// New creates a watchdog service for the given sources.
func New(sources []Source) *Service {
	return &Service{sources: sources}
}

// Start begins watching all sources and emits raw lines to the output channel.
func (s *Service) Start(ctx context.Context, output chan<- types.RawLine) {
	var wg sync.WaitGroup
	for _, src := range s.sources {
		wg.Go(func() {
			for line := range src.Read(ctx) {
				select {
				case output <- line:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	wg.Wait()
}

// SourceNames returns the names of all sources.
func (s *Service) SourceNames() []string {
	names := make([]string, len(s.sources))
	for i, src := range s.sources {
		names[i] = src.Name()
	}
	return names
}
