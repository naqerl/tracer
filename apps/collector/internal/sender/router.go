package sender

import (
	"log/slog"

	"tracer/libs/types"
)

// Router dispatches received Envelope events to registered handlers based on
// the envelope's Type field. Handlers are registered per event type.
type Router struct {
	handlers map[types.EventType]func(types.Envelope)
}

// NewRouter creates an empty Router.
func NewRouter() *Router {
	return &Router{
		handlers: make(map[types.EventType]func(types.Envelope)),
	}
}

// Register sets the handler for the given event type. If a handler was already
// registered for this type it is replaced.
func (r *Router) Register(typ types.EventType, fn func(types.Envelope)) {
	r.handlers[typ] = fn
}

// Dispatch calls the registered handler for the envelope's type. If no handler
// is registered it logs a warning at debug level (the message is not an error —
// many event types are only meaningful on the panel side).
func (r *Router) Dispatch(env types.Envelope) {
	fn, ok := r.handlers[env.Type]
	if !ok {
		slog.Debug("router: no handler for event type", "type", env.Type)
		return
	}
	fn(env)
}
