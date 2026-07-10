package sender

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"tracer/libs/types"
)

// ErrClosed is returned by Send when the reconnecting wrapper has been closed.
var ErrClosed = errors.New("reconnecting: closed")

const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
)

type sendReq struct {
	event types.Event
	resp  chan error
}

// reconnecting wraps a Client factory. Send and Recv are handled by a single
// actor goroutine that owns the current Client. No mutexes.
type reconnecting struct {
	factory func(context.Context) (Client, error)
	recv    chan types.Event
	req     chan sendReq
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once

	backoffDelay time.Duration
}

// newReconnecting starts the actor goroutine. The factory is called with the
// internal context so that Close can cancel a blocked dial.
func newReconnecting(factory func(context.Context) (Client, error)) *reconnecting {
	ctx, cancel := context.WithCancel(context.Background())
	r := &reconnecting{
		factory: factory,
		recv:    make(chan types.Event, 64),
		req:     make(chan sendReq),
		ctx:     ctx,
		cancel:  cancel,
	}
	go r.run()
	return r
}

// Send delivers an event. It blocks until the event is sent or Close is called.
// If the underlying connection fails, a new one is created transparently.
func (r *reconnecting) Send(event types.Event) error {
	resp := make(chan error, 1)
	msg := sendReq{event: event, resp: resp}
	select {
	case r.req <- msg:
	case <-r.ctx.Done():
		return ErrClosed
	}
	select {
	case err := <-resp:
		return err
	case <-r.ctx.Done():
		return ErrClosed
	}
}

// Recv returns a channel that receives events from the current inner Client.
// The channel remains stable across reconnects.
func (r *reconnecting) Recv() <-chan types.Event { return r.recv }

// Close shuts down the wrapper. It does not block — the actor goroutine
// exits when it processes the cancellation.
func (r *reconnecting) Close() {
	r.once.Do(func() { r.cancel() })
}

// run is the single actor goroutine. It owns the current Client and relays
// events to r.recv. Exits when the context is cancelled.
func (r *reconnecting) run() {
	defer close(r.recv)
	var current Client

	for {
		// Conditionally observe the current client's Recv channel.
		var recvChan <-chan types.Event
		if current != nil {
			recvChan = current.Recv()
		}

		select {
		case msg := <-r.req:
			newCurrent, err := r.dialAndSend(current, msg.event)
			if err != nil {
				msg.resp <- err
				if current != nil {
					current.Close()
				}
				current = nil
				r.waitWithBackoff()
				continue
			}
			current = newCurrent
			r.backoffDelay = 0
			msg.resp <- nil

		case evt, ok := <-recvChan:
			if !ok {
				if current != nil {
					current.Close()
				}
				current = nil
				continue
			}
			select {
			case r.recv <- evt:
			default:
				slog.Warn("reconnecting: recv channel full, dropping event")
			}

		case <-r.ctx.Done():
			if current != nil {
				current.Close()
			}
			return
		}
	}
}

// dialAndSend attempts to send the event on the provided client.
// If the client is nil or Send fails, it dials a new client and retries once.
// Returns the (possibly new) client to keep, or an error.
func (r *reconnecting) dialAndSend(current Client, event types.Event) (Client, error) {
	// Try the existing client first.
	if current != nil {
		if err := current.Send(event); err == nil {
			return current, nil
		}
		current.Close()
	}

	// Dial and send. Retry once if the first send fails (dead-on-arrival).
	c, err := r.factory(r.ctx)
	if err != nil {
		return nil, err
	}
	if err := c.Send(event); err != nil {
		c.Close()
		c2, err2 := r.factory(r.ctx)
		if err2 != nil {
			return nil, err2
		}
		if err2 := c2.Send(event); err2 != nil {
			c2.Close()
			return nil, err2
		}
		return c2, nil
	}
	return c, nil
}

// waitWithBackoff applies exponential backoff after a failed attempt.
// Returns immediately when the context is cancelled.
func (r *reconnecting) waitWithBackoff() {
	if r.backoffDelay <= 0 {
		r.backoffDelay = initialBackoff
	}
	timer := time.NewTimer(r.backoffDelay)
	select {
	case <-timer.C:
	case <-r.ctx.Done():
	}
	timer.Stop()
	r.backoffDelay *= 2
	if r.backoffDelay > maxBackoff {
		r.backoffDelay = maxBackoff
	}
}
