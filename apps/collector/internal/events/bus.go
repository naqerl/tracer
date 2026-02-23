package events

import (
	"sync"

	"github.com/ai-shift/tracer/libs/types"
)

// Bus is a fan-out event distribution hub.
type Bus struct {
	mu      sync.RWMutex
	chans   []chan types.Event
	bufSize int
}

// NewBus creates a new event bus with the specified buffer size per subscriber.
func NewBus(bufSize int) *Bus {
	return &Bus{bufSize: bufSize}
}

// Publish sends an event to all subscribers.
// Slow consumers will drop events (non-blocking).
func (b *Bus) Publish(e types.Event) {
	if e == nil {
		return
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.chans {
		select {
		case ch <- e:
		default:
			// Channel full, drop event
		}
	}
}

// Subscribe returns a read-only channel for receiving events.
func (b *Bus) Subscribe() <-chan types.Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan types.Event, b.bufSize)
	b.chans = append(b.chans, ch)
	return ch
}

// Close closes all subscriber channels and clears the subscriber list.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, ch := range b.chans {
		close(ch)
	}
	b.chans = nil
}
