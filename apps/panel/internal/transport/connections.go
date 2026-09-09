// Package transport tracks live WebSocket connections so the panel can push
// configuration to connected collectors. It does not depend on any web
// handler so both the web layer and the top-level serve wiring can use it.
package transport

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"tracer/libs/types"
	"tracer/libs/wsx"
)

// Connections tracks active WebSocket connections per collector. A collector
// may have more than one connection (e.g. during a reconnect).
type Connections struct {
	mu sync.RWMutex
	// byName indexes active collector connections by collector name.
	byName  map[string]map[*wsx.Client]struct{}
	pending map[string]chan types.FormatResultEvent
	pendMu  sync.Mutex
}

func NewConnections() *Connections {
	return &Connections{
		byName:  make(map[string]map[*wsx.Client]struct{}),
		pending: make(map[string]chan types.FormatResultEvent),
	}
}

func (c *Connections) Add(collector string, client *wsx.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	set := c.byName[collector]
	if set == nil {
		set = make(map[*wsx.Client]struct{})
		c.byName[collector] = set
	}
	set[client] = struct{}{}
}

func (c *Connections) Remove(collector string, client *wsx.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	set := c.byName[collector]
	if set == nil {
		return
	}
	delete(set, client)
	if len(set) == 0 {
		delete(c.byName, collector)
	}
}

// PushCollectorConfig sends the stored configuration to all live connections of a collector.
func (c *Connections) PushCollectorConfig(collector, configJSON string) {
	c.mu.RLock()
	set := c.byName[collector]
	clients := make([]*wsx.Client, 0, len(set))
	for cl := range set {
		clients = append(clients, cl)
	}
	c.mu.RUnlock()

	for _, cl := range clients {
		if err := cl.Send(types.ConfigEvent{Type: types.EventTypeConfig, Config: configJSON}); err != nil {
			slog.Warn("failed to push config to collector connection", "collector", collector, "error", err)
		}
	}
}

// RegisterFormatResult registers a channel to receive the format guess result
// for the given request ID.
func (c *Connections) RegisterFormatResult(requestID string) chan types.FormatResultEvent {
	ch := make(chan types.FormatResultEvent, 1)
	c.pendMu.Lock()
	c.pending[requestID] = ch
	c.pendMu.Unlock()
	return ch
}

// HandleFormatResult delivers a format guess result to the waiting caller, if any.
func (c *Connections) HandleFormatResult(result types.FormatResultEvent) {
	c.pendMu.Lock()
	ch, ok := c.pending[result.RequestID]
	if ok {
		delete(c.pending, result.RequestID)
	}
	c.pendMu.Unlock()
	if ok {
		select {
		case ch <- result:
		default:
		}
	}
}

// SendFormatRequest sends a format guess request to the first available
// connection of the named collector. Returns an error if the collector is offline.
func (c *Connections) SendFormatRequest(collector string, req types.FormatRequestEvent) error {
	c.mu.RLock()
	set := c.byName[collector]
	var client *wsx.Client
	for cl := range set {
		client = cl
		break
	}
	c.mu.RUnlock()

	if client == nil {
		return fmt.Errorf("collector %s is not connected", collector)
	}
	return client.Send(req)
}

// WaitForFormatResult blocks until the collector responds or ctx expires.
func (c *Connections) WaitForFormatResult(ctx context.Context, ch chan types.FormatResultEvent) (types.FormatResultEvent, error) {
	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return types.FormatResultEvent{}, ctx.Err()
	}
}

// GuessFormat sends a format guess request and waits for the result.
// Implements the panel service's FormatGuesser interface.
func (c *Connections) GuessFormat(ctx context.Context, collector string, req types.FormatRequestEvent) (types.FormatResultEvent, error) {
	ch := c.RegisterFormatResult(req.RequestID)
	if err := c.SendFormatRequest(collector, req); err != nil {
		return types.FormatResultEvent{}, err
	}
	return c.WaitForFormatResult(ctx, ch)
}
