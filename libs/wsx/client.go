package wsx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/types"
)

// Default configuration values.
const (
	DefaultHandshakeTimeout = 10 * time.Second
	DefaultWriteTimeout     = 10 * time.Second
	DefaultPingInterval     = 30 * time.Second
	DefaultPongWait         = 60 * time.Second
	DefaultReconnectDelay   = 5 * time.Second
	DefaultMaxReconnectWait = 5 * time.Minute
	DefaultReadLimit        = 10 * 1024 * 1024 // 10 MB
)

// ClientOption configures the Client. Options are applied in order.
type ClientOption func(*cfg)

// cfg holds all configuration for a Client.
type cfg struct {
	url              string
	headers          http.Header
	conn             *websocket.Conn // pre-existing connection (server mode)
	handshakeTimeout time.Duration
	writeTimeout     time.Duration
	pingInterval     time.Duration
	pongWait         time.Duration
	reconnectDelay   time.Duration
	maxReconnectWait time.Duration
	readLimit        int64
	onConnect        func(ctx context.Context) error
	onDisconnect     func()
}

// setDefaults fills zero-valued fields with sensible defaults.
func (c *cfg) setDefaults() {
	if c.handshakeTimeout <= 0 {
		c.handshakeTimeout = DefaultHandshakeTimeout
	}
	if c.writeTimeout <= 0 {
		c.writeTimeout = DefaultWriteTimeout
	}
	if c.pingInterval <= 0 {
		c.pingInterval = DefaultPingInterval
	}
	if c.pongWait <= 0 {
		c.pongWait = DefaultPongWait
	}
	if c.reconnectDelay <= 0 {
		c.reconnectDelay = DefaultReconnectDelay
	}
	if c.maxReconnectWait <= 0 {
		c.maxReconnectWait = DefaultMaxReconnectWait
	}
	if c.readLimit <= 0 {
		c.readLimit = DefaultReadLimit
	}
}

// ── Option functions ───────────────────────────────────────────────────────────

// WithURL sets the WebSocket server endpoint (e.g. "ws://host/ws").
// When set, the client dials the URL and reconnects on failure (client mode).
func WithURL(url string) ClientOption {
	return func(c *cfg) { c.url = url }
}

// WithHeaders sets the HTTP headers sent during the initial handshake.
func WithHeaders(headers http.Header) ClientOption {
	return func(c *cfg) { c.headers = headers }
}

// WithConn wraps an already-upgraded WebSocket connection.
// When set, the client runs in server mode (no reconnection, no dial).
func WithConn(raw *websocket.Conn) ClientOption {
	return func(c *cfg) { c.conn = raw }
}

// WithHandshakeTimeout sets the dial handshake timeout.
func WithHandshakeTimeout(d time.Duration) ClientOption {
	return func(c *cfg) { c.handshakeTimeout = d }
}

// WithWriteTimeout sets the write deadline for each outgoing frame.
func WithWriteTimeout(d time.Duration) ClientOption {
	return func(c *cfg) { c.writeTimeout = d }
}

// WithPingInterval sets the interval between keepalive pings.
func WithPingInterval(d time.Duration) ClientOption {
	return func(c *cfg) { c.pingInterval = d }
}

// WithPongWait sets the time to wait for a pong before considering the
// connection dead (also used as the read deadline).
func WithPongWait(d time.Duration) ClientOption {
	return func(c *cfg) { c.pongWait = d }
}

// WithReconnectDelay sets the initial delay before reconnecting.
func WithReconnectDelay(d time.Duration) ClientOption {
	return func(c *cfg) { c.reconnectDelay = d }
}

// WithMaxReconnectWait sets the maximum backoff delay between reconnect attempts.
func WithMaxReconnectWait(d time.Duration) ClientOption {
	return func(c *cfg) { c.maxReconnectWait = d }
}

// WithReadLimit sets the maximum message size in bytes (0 = no limit).
func WithReadLimit(limit int64) ClientOption {
	return func(c *cfg) { c.readLimit = limit }
}

// WithOnConnect sets a callback called after a successful connection.
// If it returns an error, the connection is closed and a reconnect is attempted.
func WithOnConnect(fn func(ctx context.Context) error) ClientOption {
	return func(c *cfg) { c.onConnect = fn }
}

// WithOnDisconnect sets a callback called when the connection is lost.
func WithOnDisconnect(fn func()) ClientOption {
	return func(c *cfg) { c.onDisconnect = fn }
}

// ── Client ─────────────────────────────────────────────────────────────────────

// Client manages a WebSocket connection and provides a clean
// Send/Recv API operating on the Event interface.
//
// Two modes:
//   - Client mode: created via New with WithURL.
//     The client dials the URL, reconnects on failure, and manages the
//     connection lifecycle internally.
//   - Server mode: created via New with WithConn, wrapping an already-upgraded
//     connection (e.g. from the panel's HTTP handler). No reconnection.
type Client struct {
	cfg      cfg
	conn     *conn
	connMu   sync.RWMutex
	dialer   websocket.Dialer
	recv     chan types.Event
	closed   bool
	closedMu sync.Mutex
}

// New creates a Client with the given options.
//
//	client := wsx.New(
//	    wsx.WithURL("ws://panel/ws"),
//	    wsx.WithHeaders(headers),
//	    wsx.WithOnConnect(func(ctx) error { ... }),
//	)
func New(opts ...ClientOption) *Client {
	var c cfg
	for _, opt := range opts {
		opt(&c)
	}
	c.setDefaults()

	cl := &Client{
		cfg: c,
		dialer: websocket.Dialer{
			HandshakeTimeout: c.handshakeTimeout,
		},
		recv: make(chan types.Event, 64),
	}

	// Server mode: pre-wrapped connection.
	if c.conn != nil {
		wrapped := newConn(c.conn, c.writeTimeout)
		wrapped.setReadLimit(c.readLimit)
		wrapped.setReadDeadline(time.Now().Add(c.pongWait))
		wrapped.setPongHandler(func(string) error {
			wrapped.setReadDeadline(time.Now().Add(c.pongWait))
			return nil
		})
		cl.connMu.Lock()
		cl.conn = wrapped
		cl.connMu.Unlock()
	}

	return cl
}

// Close shuts down the connection and releases resources.
// Safe to call multiple times.
func (c *Client) Close() {
	c.close()
}

// Send marshals an event to JSON and sends it over the WebSocket connection.
// It is safe for concurrent use. If not connected, it returns an error.
func (c *Client) Send(event types.Event) error {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()
	if conn == nil {
		return errors.New("wsx: not connected")
	}
	return conn.writeJSON(event)
}

// Recv returns a channel of received events. The channel is closed when the
// connection is closed or the context is cancelled.
func (c *Client) Recv() <-chan types.Event {
	return c.recv
}

// Start begins the connection loop. It blocks until the context is cancelled.
// For client mode (WithURL), it reconnects with exponential backoff on failure.
// For server mode (WithConn), it runs until the wrapped connection drops.
func (c *Client) Start(ctx context.Context) error {
	// Server mode: already have a connection.
	if c.conn != nil {
		return c.runServer(ctx)
	}
	return c.runClient(ctx)
}

// runClient connects, reconnects on failure, and manages the lifecycle.
func (c *Client) runClient(ctx context.Context) error {
	reconnectWait := c.cfg.reconnectDelay

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := c.connect(ctx); err != nil {
			slog.Error("wsx: failed to connect", "url", c.cfg.url, "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(reconnectWait):
			}
			reconnectWait = time.Duration(math.Min(
				float64(reconnectWait)*2,
				float64(c.cfg.maxReconnectWait),
			))
			continue
		}

		reconnectWait = c.cfg.reconnectDelay
		slog.Info("wsx: connected", "url", c.cfg.url)

		if c.cfg.onConnect != nil {
			if err := c.cfg.onConnect(ctx); err != nil {
				slog.Error("wsx: OnConnect failed, reconnecting", "error", err)
				c.close()
				continue
			}
		}

		err := c.run(ctx)
		c.close()
		if err != nil {
			slog.Warn("wsx: connection lost", "error", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		continue
	}
}

// runServer runs the read loop for a pre-existing connection (server mode).
func (c *Client) runServer(ctx context.Context) error {
	if c.cfg.onConnect != nil {
		if err := c.cfg.onConnect(ctx); err != nil {
			slog.Error("wsx: OnConnect failed", "error", err)
			c.close()
			return err
		}
	}
	err := c.run(ctx)
	c.close()
	return err
}

func (c *Client) connect(ctx context.Context) error {
	raw, _, err := c.dialer.DialContext(ctx, c.cfg.url, c.cfg.headers)
	if err != nil {
		return err
	}

	wrapped := newConn(raw, c.cfg.writeTimeout)
	wrapped.setReadLimit(c.cfg.readLimit)
	wrapped.setReadDeadline(time.Now().Add(c.cfg.pongWait))
	wrapped.setPongHandler(func(string) error {
		wrapped.setReadDeadline(time.Now().Add(c.cfg.pongWait))
		return nil
	})

	c.connMu.Lock()
	c.conn = wrapped
	c.connMu.Unlock()
	return nil
}

func (c *Client) run(ctx context.Context) error {
	pingTicker := time.NewTicker(c.cfg.pingInterval)
	defer pingTicker.Stop()

	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()

	readErr := make(chan error, 1)
	go c.readLoop(readErr, conn)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case <-pingTicker.C:
			if err := conn.ping(time.Now().Add(c.cfg.writeTimeout)); err != nil {
				return err
			}
		}
	}
}

func (c *Client) readLoop(errCh chan<- error, conn *conn) {
	for {
		_, data, err := conn.readMessage()
		if err != nil {
			errCh <- err
			return
		}
		evt, err := decodeEvent(data)
		if err != nil {
			slog.Warn("wsx: failed to decode event", "error", err)
			continue
		}
		select {
		case c.recv <- evt:
		default:
			slog.Warn("wsx: recv channel full, dropping event")
		}
	}
}

// decodeEvent unmarshals a JSON message into the appropriate Event type
// based on the "type" field.
func decodeEvent(data []byte) (types.Event, error) {
	var envelope struct {
		Type types.EventType `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	switch envelope.Type {
	case types.EventTypeRegister:
		var msg types.RegisterMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return nil, err
		}
		return msg, nil
	default:
		var env types.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			return nil, err
		}
		return env, nil
	}
}

// close sends a close frame and cleans up the connection.
func (c *Client) close() {
	c.closedMu.Lock()
	if c.closed {
		c.closedMu.Unlock()
		return
	}
	c.closed = true
	c.closedMu.Unlock()

	c.connMu.Lock()
	if c.conn != nil {
		c.conn.close()
		c.conn = nil
	}
	c.connMu.Unlock()
	if c.cfg.onDisconnect != nil {
		c.cfg.onDisconnect()
	}
	// Signal to Recv consumers that the connection is gone.
	// Multiple close calls are guarded by c.closed.
	close(c.recv)
}
