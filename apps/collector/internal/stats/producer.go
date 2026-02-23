package stats

import (
	"context"
	"time"

	"github.com/ai-shift/tracer/apps/collector/internal/events"
	"github.com/ai-shift/tracer/libs/types"
)

// Producer generates stats events at regular intervals.
type Producer struct {
	collector *Collector
	interval  time.Duration
	bus       *events.Bus
}

// NewProducer creates a stats producer.
func NewProducer(collector *Collector, interval time.Duration, bus *events.Bus) *Producer {
	return &Producer{
		collector: collector,
		interval:  interval,
		bus:       bus,
	}
}

// Start begins producing stats events until context is cancelled.
func (p *Producer) Start(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	// Send initial stats immediately
	p.publish()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.publish()
		}
	}
}

func (p *Producer) publish() {
	stats, err := p.collector.Collect()
	if err != nil {
		return
	}

	p.bus.Publish(types.StatsEvent{
		Time: time.Now(),
		Data: *stats,
	})
}
