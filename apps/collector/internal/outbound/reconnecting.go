package outbound

import (
	"sync"

	"tracer/libs/types"
)

// reconnecting wraps a Client factory, automatically recreating the client
// on Send failure. Satisfies the Client interface.
type reconnecting struct {
	factory func() (Client, error)
	current Client
	mu      sync.RWMutex
}

func newReconnecting(factory func() (Client, error)) *reconnecting {
	return &reconnecting{factory: factory}
}

// Send tries the current client. If Send fails, it closes the dead client,
// creates a new one via the factory, and retries the Send once.
func (r *reconnecting) Send(event types.Event) error {
	r.mu.RLock()
	cur := r.current
	r.mu.RUnlock()

	if cur != nil {
		if err := cur.Send(event); err == nil {
			return nil
		}
		cur.Close()
	}

	// Create a fresh client and retry the send.
	newC, err := r.factory()
	if err != nil {
		return err
	}
	if err := newC.Send(event); err != nil {
		newC.Close()
		return err
	}

	r.mu.Lock()
	// Replace current; Close the old one if it was replaced concurrently.
	if r.current != nil {
		r.current.Close()
	}
	r.current = newC
	r.mu.Unlock()
	return nil
}

// Recv delegates to the current client. Returns a closed channel if nil.
func (r *reconnecting) Recv() <-chan types.Event {
	r.mu.RLock()
	cur := r.current
	r.mu.RUnlock()
	if cur == nil {
		ch := make(chan types.Event)
		close(ch)
		return ch
	}
	return cur.Recv()
}

// Close shuts down the current client, if any.
func (r *reconnecting) Close() {
	r.mu.Lock()
	cur := r.current
	r.current = nil
	r.mu.Unlock()
	if cur != nil {
		cur.Close()
	}
}
