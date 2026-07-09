package outbound

import (
	"errors"
	"sync"

	"tracer/libs/types"
)

// reconnecting wraps a Client factory, transparently reconnecting on Send
// failure. Recv returns a channel that seamlessly receives events from
// whichever inner client is active.
type reconnecting struct {
	factory func() (Client, error)
	current Client
	mu      sync.RWMutex
	recv    chan types.Event
	notify  chan struct{} // 1-buffered; signals pipe that current may be non-nil
	alive   bool
}

func newReconnecting(factory func() (Client, error)) *reconnecting {
	r := &reconnecting{
		factory: factory,
		recv:    make(chan types.Event, 64),
		notify:  make(chan struct{}, 1),
		alive:   true,
	}
	go r.pipe()
	return r
}

// pipe reads from the current client's Recv channel and relays events to
// r.recv. It runs until Close is called.
func (r *reconnecting) pipe() {
	defer close(r.recv)

	for {
		r.mu.RLock()
		cur := r.current
		alive := r.alive
		r.mu.RUnlock()

		if !alive {
			return
		}
		if cur == nil {
			// Wait for a client to be set (by Send).
			<-r.notify
			continue
		}

		select {
		case evt, ok := <-cur.Recv():
			if !ok {
				r.mu.Lock()
				if r.current == cur {
					r.current = nil
				}
				r.mu.Unlock()
				continue
			}
			select {
			case r.recv <- evt:
			default:
			}
		case <-r.notify:
			// Client was replaced; loop to re-read r.current.
		}
	}
}

// Send delivers an event. If the underlying connection fails, it discards
// the dead client, creates a fresh one, and retries exactly once.
func (r *reconnecting) Send(event types.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !r.alive {
		return errors.New("reconnecting: closed")
	}

	if r.current != nil {
		if err := r.current.Send(event); err == nil {
			return nil
		}
		r.current.Close()
		r.current = nil
	}

	newC, err := r.factory()
	if err != nil {
		return err
	}
	if err := newC.Send(event); err != nil {
		newC.Close()
		return err
	}
	r.current = newC

	// Wake pipe if it's waiting for a client.
	select {
	case r.notify <- struct{}{}:
	default:
	}

	return nil
}

// Recv returns a channel that receives events from the current inner client.
// The channel remains stable across reconnects.
func (r *reconnecting) Recv() <-chan types.Event { return r.recv }

// Close shuts down the wrapper. Pending events in the buffer may still be
// received by consumers.
func (r *reconnecting) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.alive {
		return
	}
	r.alive = false
	if r.current != nil {
		r.current.Close()
		r.current = nil
	}
	// Wake pipe so it sees alive==false and exits.
	select {
	case r.notify <- struct{}{}:
	default:
	}
}
