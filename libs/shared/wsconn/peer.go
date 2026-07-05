package wsconn

import (
	"fmt"
	"time"

	"tracer/libs/types"
)

const (
	// peerPingInterval is how often the peer sends a WebSocket ping
	// to keep the connection alive and detect half-open connections.
	peerPingInterval = 30 * time.Second
)

// Peer wraps a Conn with an event handler that is called for every
// incoming event. It also manages a periodic ping to keep the
// connection alive.
//
// Both the collector (1 connection) and the panel (N connections)
// use Peer as the shared abstraction for reading events from the
// WebSocket and writing events to it.
type Peer struct {
	conn    *Conn
	handler types.EventHandler
}

// NewPeer creates a Peer, starts the read-dispatch loop, and starts a
// periodic ping loop. The handler is called for each event received
// from the underlying connection.
func NewPeer(conn *Conn, handler types.EventHandler) *Peer {
	p := &Peer{
		conn:    conn,
		handler: handler,
	}
	go p.readLoop()
	return p
}

// Send sends an event to the peer. It blocks until the event is
// written to the outgoing buffer or the connection is closed.
func (p *Peer) Send(evt types.Event) error {
	select {
	case p.conn.Outgoing() <- evt:
		return nil
	case <-p.conn.Closed():
		return fmt.Errorf("connection closed")
	}
}

// Ping sends a WebSocket ping control frame.
func (p *Peer) Ping() error { return p.conn.Ping() }

// Close closes the underlying connection.
func (p *Peer) Close() { p.conn.Close() }

// Closed returns a channel that is closed when the connection is lost.
func (p *Peer) Closed() <-chan struct{} { return p.conn.Closed() }

// readLoop reads events from Incoming() and dispatches them to the
// handler. It also sends periodic pings to keep the connection alive.
// It exits when the underlying connection is closed.
func (p *Peer) readLoop() {
	pingTicker := time.NewTicker(peerPingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case evt, ok := <-p.conn.Incoming():
			if !ok {
				return
			}
			p.handler(evt)
		case <-pingTicker.C:
			if err := p.conn.Ping(); err != nil {
				return
			}
		}
	}
}
