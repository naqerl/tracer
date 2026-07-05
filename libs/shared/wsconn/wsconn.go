// Package wsconn provides a channel-based wrapper around gorilla/websocket.Conn.
// It satisfies the single-writer requirement internally and exposes two channels:
//   - Incoming: receives messages from the peer.
//   - Outgoing: sends messages to the peer. All values sent are JSON-marshalled
//     by the write loop before being written to the WebSocket.
//
// Both the collector (to connect to panel) and the panel (to manage collector
// connections) use this package.
package wsconn

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/types"
)

const (
	// IncomingCap is the buffer size for incoming messages.
	IncomingCap = 8
	// OutgoingCap is the buffer size for outgoing messages.
	OutgoingCap = 32
	// WriteTimeout is the deadline for each write operation.
	WriteTimeout = 10 * time.Second
	// PongWait is how long to wait for a pong before considering the
	// connection dead.
	PongWait = 60 * time.Second
)

// Conn wraps a gorilla/websocket.Conn with channel-based I/O.
// All writes are serialized through a single goroutine.
// All reads are serialized through a single goroutine.
type Conn struct {
	conn      *websocket.Conn
	incoming  chan types.Event
	outgoing  chan types.Event
	wDone     chan struct{} // closed when writeLoop exits
	rDone     chan struct{} // closed when readLoop exits
	closeOnce sync.Once
}

// New wraps an existing WebSocket connection.
// It starts background goroutines for reading and writing.
func New(conn *websocket.Conn) *Conn {
	c := &Conn{
		conn:     conn,
		incoming: make(chan types.Event, IncomingCap),
		outgoing: make(chan types.Event, OutgoingCap),
		wDone:    make(chan struct{}),
		rDone:    make(chan struct{}),
	}

	conn.SetReadDeadline(time.Now().Add(PongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(PongWait))
		return nil
	})

	go c.writeLoop()
	go c.readLoop()

	return c
}

// Incoming returns a receive-only channel of messages received from the peer.
// The channel is closed when the connection is closed or an error occurs.
func (c *Conn) Incoming() <-chan types.Event { return c.incoming }

// Outgoing returns a send-only channel for messages to send to the peer.
// Any value sent to this channel is JSON-marshalled by the write loop before
// being written to the WebSocket.
func (c *Conn) Outgoing() chan<- types.Event { return c.outgoing }

// Closed returns a channel that is closed when the write loop exits (connection lost or shutdown).
func (c *Conn) Closed() <-chan struct{} { return c.wDone }

// writeLoop is the single goroutine that writes to the underlying conn.
func (c *Conn) writeLoop() {
	defer close(c.wDone)
	for v := range c.outgoing {
		data, err := json.Marshal(v)
		if err != nil {
			slog.Error("wsconn: failed to marshal outgoing message", "error", err)
			continue
		}

		c.conn.SetWriteDeadline(time.Now().Add(WriteTimeout))
		if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
			slog.Debug("WebSocket write failed", "error", err)
			return
		}
	}
}

// readLoop reads messages from the underlying conn and sends them to Incoming.
func (c *Conn) readLoop() {
	defer close(c.incoming)
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}

		evt, err := types.FromJSON(data)
		if err != nil {
			slog.Warn("wsconn: failed to parse incoming message", "error", err)
			continue
		}

		select {
		case c.incoming <- evt:
		default:
			slog.Warn("Incoming message buffer full, dropping message")
		}
	}
}

// Ping sends a WebSocket ping control frame.
// gorilla's WriteControl can be called concurrently with WriteMessage,
// so this bypasses the outgoing channel.
func (c *Conn) Ping() error {
	return c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(WriteTimeout))
}

// Close gracefully shuts down the connection.
func (c *Conn) Close() {
	c.closeOnce.Do(func() {
		// Signal writeLoop to exit by closing the outgoing channel.
		close(c.outgoing)
		<-c.wDone

		// Close the underlying conn to unblock readLoop.
		c.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(WriteTimeout),
		)
		c.conn.Close()
		<-c.rDone
	})
}
