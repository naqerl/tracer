// Package wsx provides a thread-safe WebSocket connection wrapper and
// a reconnecting client with exponential backoff.
package wsx

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Conn is a thread-safe wrapper around a websocket connection.
// It serializes writes with a mutex so that concurrent callers (e.g.
// keepalive pings and event sends) do not race on the underlying conn.
type Conn struct {
	conn         *websocket.Conn
	writeTimeout time.Duration
	mu           sync.Mutex
	closed       bool
}

// NewConn wraps an existing websocket connection with a write deadline.
func NewConn(conn *websocket.Conn, writeTimeout time.Duration) *Conn {
	return &Conn{
		conn:         conn,
		writeTimeout: writeTimeout,
	}
}

// WriteMessage sends a message on the connection. It is safe for concurrent use.
func (c *Conn) WriteMessage(messageType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeTimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	}
	return c.conn.WriteMessage(messageType, data)
}

// WriteJSON marshals v as JSON and sends it as a text message.
// It is safe for concurrent use.
func (c *Conn) WriteJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeTimeout > 0 {
		c.conn.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	}
	return c.conn.WriteJSON(v)
}

// WriteControl sends a control frame. It is safe for concurrent use.
func (c *Conn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteControl(messageType, data, deadline)
}

// ReadMessage reads the next message from the connection.
// The caller MUST NOT call ReadMessage concurrently with itself.
func (c *Conn) ReadMessage() (messageType int, p []byte, err error) {
	return c.conn.ReadMessage()
}

// SetReadDeadline sets the read deadline on the underlying connection.
func (c *Conn) SetReadDeadline(t time.Time) {
	c.conn.SetReadDeadline(t)
}

// SetReadLimit sets the maximum message size in bytes.
func (c *Conn) SetReadLimit(limit int64) {
	c.conn.SetReadLimit(limit)
}

// SetPongHandler sets the handler for pong messages.
func (c *Conn) SetPongHandler(h func(string) error) {
	c.conn.SetPongHandler(h)
}

// Close sends a close frame and closes the underlying connection.
// It is safe to call multiple times; subsequent calls are no-ops.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	// Best-effort close handshake.
	msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	_ = c.conn.WriteControl(websocket.CloseMessage, msg, time.Now().Add(2*time.Second))
	return c.conn.Close()
}

// UnderlyingConn returns the raw websocket connection for advanced use.
// Callers must not write concurrently with Conn's methods.
func (c *Conn) UnderlyingConn() *websocket.Conn {
	return c.conn
}

// Ping sends a ping control frame.
func (c *Conn) Ping(deadline time.Time) error {
	return c.WriteControl(websocket.PingMessage, nil, deadline)
}

// WriteCloseMessage sends a close control frame with the given code and text.
func (c *Conn) WriteCloseMessage(code int, text string) error {
	return c.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(code, text),
		time.Now().Add(2*time.Second),
	)
}

// IsClosed returns true if Close has been called.
func (c *Conn) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// ReadJSON reads the next message and unmarshals it into v.
func (c *Conn) ReadJSON(v any) error {
	return c.conn.ReadJSON(v)
}

// SendText is a convenience for WriteMessage with TextMessage.
func (c *Conn) SendText(data string) error {
	return c.WriteMessage(websocket.TextMessage, []byte(data))
}

// SendJSON is a type-safe alias for WriteJSON so callers don't need to
// import websocket constants.
func (c *Conn) SendJSON(v any) error {
	return c.WriteJSON(v)
}
