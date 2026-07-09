package wsx

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// conn is a thread-safe wrapper around a websocket.Conn.
// It serializes writes with a mutex so that concurrent callers
// (e.g. keepalive pings and event sends) do not race on the underlying conn.
type conn struct {
	raw          *websocket.Conn
	writeTimeout time.Duration
	mu           sync.Mutex
	closed       bool
}

// newConn wraps an existing websocket connection.
func newConn(raw *websocket.Conn, writeTimeout time.Duration) *conn {
	return &conn{
		raw:          raw,
		writeTimeout: writeTimeout,
	}
}

// writeJSON marshals v as JSON and sends it as a text message. Safe for concurrent use.
func (c *conn) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writeTimeout > 0 {
		c.raw.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	}
	return c.raw.WriteJSON(v)
}

// writeControl sends a control frame. Safe for concurrent use.
func (c *conn) writeControl(messageType int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.raw.WriteControl(messageType, data, deadline)
}

// readMessage reads the next message from the connection.
// The caller MUST NOT call readMessage concurrently with itself.
func (c *conn) readMessage() (messageType int, p []byte, err error) {
	return c.raw.ReadMessage()
}

// setReadDeadline sets the read deadline on the underlying connection.
func (c *conn) setReadDeadline(t time.Time) {
	c.raw.SetReadDeadline(t)
}

// setReadLimit sets the maximum message size in bytes.
func (c *conn) setReadLimit(limit int64) {
	c.raw.SetReadLimit(limit)
}

// setPongHandler sets the handler for pong messages.
func (c *conn) setPongHandler(h func(string) error) {
	c.raw.SetPongHandler(h)
}

// close sends a close frame and closes the underlying connection.
// It is safe to call multiple times; subsequent calls are no-ops.
func (c *conn) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	_ = c.raw.WriteControl(websocket.CloseMessage, msg, time.Now().Add(2*time.Second))
	return c.raw.Close()
}

// ping sends a ping control frame.
func (c *conn) ping(deadline time.Time) error {
	return c.writeControl(websocket.PingMessage, nil, deadline)
}


