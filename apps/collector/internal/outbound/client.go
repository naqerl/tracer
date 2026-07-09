package outbound

import "tracer/libs/types"

// Client is the interface the outbound service uses to send and receive events.
// Both *wsx.Client and the internal reconnecting wrapper satisfy it.
type Client interface {
	Send(event types.Event) error
	Recv() <-chan types.Event
	Close()
}
