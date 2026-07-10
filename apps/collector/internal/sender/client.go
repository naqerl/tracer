package sender

import "tracer/libs/types"

// Client is the interface the sender service uses to send and receive events.
type Client interface {
	Send(event types.Event) error
	Recv() <-chan types.Event
	Close()
}
