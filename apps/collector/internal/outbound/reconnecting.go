package outbound

import (
	"context"
	"errors"
	"sync"

	"tracer/libs/types"
)

type sendReq struct {
	event types.Event
	resp  chan error
}

// reconnecting wraps a Client factory. Send and Recv are handled by a single
// actor goroutine that owns the current Client. No mutexes.
type reconnecting struct {
	factory func() (Client, error)
	recv    chan types.Event
	req     chan sendReq
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once
}

func newReconnecting(factory func() (Client, error)) *reconnecting {
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

// run is the single actor goroutine. It owns current Client and relays events
// to r.recv. Exits when the context is cancelled.
func (r *reconnecting) run() {
	defer close(r.recv)
	var current Client

	for {
		if current == nil {
			select {
			case msg := <-r.req:
				c, err := r.factory()
				if err != nil {
					msg.resp <- err
					continue
				}
				if err := c.Send(msg.event); err != nil {
					c.Close()
					// First attempt failed — retry once with a fresh client.
					newC, err2 := r.factory()
					if err2 != nil {
						msg.resp <- err2
						continue
					}
					if err2 := newC.Send(msg.event); err2 != nil {
						newC.Close()
						msg.resp <- err2
						continue
					}
					current = newC
					msg.resp <- nil
					continue
				}
				current = c
				msg.resp <- nil
			case <-r.ctx.Done():
				return
			}
			continue
		}

		select {
		case msg := <-r.req:
			err := current.Send(msg.event)
			if err == nil {
				msg.resp <- nil
				continue
			}
			// Connection lost — create new client and retry once.
			current.Close()
			current = nil
			newC, err2 := r.factory()
			if err2 != nil {
				msg.resp <- err2
				continue
			}
			if err2 := newC.Send(msg.event); err2 != nil {
				newC.Close()
				msg.resp <- err2
				continue
			}
			current = newC
			msg.resp <- nil

		case evt, ok := <-current.Recv():
			if !ok {
				current = nil
				continue
			}
			select {
			case r.recv <- evt:
			default:
			}

		case <-r.ctx.Done():
			if current != nil {
				current.Close()
			}
			return
		}
	}
}

// Send delivers an event. It blocks until the event is sent or Close is called.
// If the underlying connection fails, a new one is created transparently.
func (r *reconnecting) Send(event types.Event) error {
	resp := make(chan error, 1)
	msg := sendReq{event: event, resp: resp}
	select {
	case r.req <- msg:
	case <-r.ctx.Done():
		return errors.New("reconnecting: closed")
	}
	select {
	case err := <-resp:
		return err
	case <-r.ctx.Done():
		return errors.New("reconnecting: closed")
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
