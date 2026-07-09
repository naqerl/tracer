package wsx

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/types"
)

// Default configuration values.
const (
	DefaultWriteTimeout = 10 * time.Second
	DefaultPingInterval = 30 * time.Second
	DefaultPongWait     = 60 * time.Second
	DefaultReadLimit    = 10 * 1024 * 1024 // 10 MB
)

// ClientOption configures the Client. Options are applied in order.
type ClientOption func(*clientConfig)

// clientConfig holds configuration for a Client.
type clientConfig struct {
	headers          http.Header
	writeTimeout     time.Duration
	pingInterval     time.Duration
	pongWait         time.Duration
	pongCallback     func()
	readLimit        int64
	handshakeTimeout time.Duration
}

func (c *clientConfig) setDefaults() {
	if c.writeTimeout <= 0 {
		c.writeTimeout = DefaultWriteTimeout
	}
	if c.pingInterval <= 0 {
		c.pingInterval = DefaultPingInterval
	}
	if c.pongWait <= 0 {
		c.pongWait = DefaultPongWait
	}
	if c.readLimit <= 0 {
		c.readLimit = DefaultReadLimit
	}
	if c.handshakeTimeout <= 0 {
		c.handshakeTimeout = 10 * time.Second
	}
}

// ── Option functions ───────────────────────────────────────────────────────────

// WithHeaders sets the HTTP headers sent during the initial handshake.
func WithHeaders(headers http.Header) ClientOption {
	return func(c *clientConfig) { c.headers = headers }
}

// WithWriteTimeout sets the write deadline for each outgoing frame.
func WithWriteTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.writeTimeout = d }
}

// WithPingInterval sets the interval between keepalive pings.
func WithPingInterval(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.pingInterval = d }
}

// WithPongWait sets the time to wait for a pong before considering the
// connection dead (also used as the read deadline).
func WithPongWait(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.pongWait = d }
}

// WithReadLimit sets the maximum message size in bytes (0 = no limit).
func WithReadLimit(limit int64) ClientOption {
	return func(c *clientConfig) { c.readLimit = limit }
}

// WithPongCallback sets a function to call on each pong (in addition to
// resetting the read deadline). Used by the panel to track connection liveness.
func WithPongCallback(fn func()) ClientOption {
	return func(c *clientConfig) { c.pongCallback = fn }
}

// WithHandshakeTimeout sets the dial handshake timeout.
func WithHandshakeTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.handshakeTimeout = d }
}

// ── Client ─────────────────────────────────────────────────────────────────────

// Client is a single-session WebSocket client with a Send/Recv/Close API.
// No reconnection, no callbacks. Create via New (dial) or Upgrade (HTTP upgrade).
//
// The read loop and ping loop start automatically on construction and shut down
// on Close.
type Client struct {
	conn         *conn
	recv         chan types.Event
	closed       chan struct{}
	closeOnce    sync.Once
	writeTimeout time.Duration
}

// New dials the given WebSocket URL and returns an active Client.
func New(url string, opts ...ClientOption) (*Client, error) {
	var cfg clientConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.setDefaults()

	dialer := websocket.Dialer{HandshakeTimeout: cfg.handshakeTimeout}
	raw, _, err := dialer.Dial(url, cfg.headers)
	if err != nil {
		return nil, err
	}
	return newClient(raw, &cfg), nil
}

// Upgrade creates a Client from an HTTP WebSocket upgrade request.
// The caller passes the HTTP response writer and request from the handler.
func Upgrade(w http.ResponseWriter, r *http.Request, opts ...ClientOption) (*Client, error) {
	var upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}

	var cfg clientConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	cfg.setDefaults()

	raw, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return nil, err
	}
	return newClient(raw, &cfg), nil
}

// newClient wraps a raw gorilla connection and starts the read and ping loops.
func newClient(raw *websocket.Conn, cfg *clientConfig) *Client {
	wc := newConn(raw, cfg.writeTimeout)
	wc.setReadLimit(cfg.readLimit)
	wc.setReadDeadline(time.Now().Add(cfg.pongWait))

	if cfg.pongCallback != nil {
		wc.setPongHandler(func(string) error {
			wc.setReadDeadline(time.Now().Add(cfg.pongWait))
			cfg.pongCallback()
			return nil
		})
	} else {
		wc.setPongHandler(func(string) error {
			wc.setReadDeadline(time.Now().Add(cfg.pongWait))
			return nil
		})
	}

	c := &Client{
		conn:         wc,
		recv:         make(chan types.Event, 64),
		closed:       make(chan struct{}),
		writeTimeout: cfg.writeTimeout,
	}
	go c.readLoop()
	go c.pingLoop(cfg.pingInterval)
	return c
}

// Close shuts down the connection and releases resources.
// Safe to call multiple times.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.conn.close()
	})
}

// Send marshals an event to JSON and sends it over the WebSocket connection.
// Safe for concurrent use.
func (c *Client) Send(event types.Event) error {
	return c.conn.writeJSON(event)
}

// Recv returns a channel of received events. The channel is closed when the
// connection drops or Close is called.
func (c *Client) Recv() <-chan types.Event {
	return c.recv
}

// readLoop reads messages from the WebSocket connection and sends them to the
// recv channel. Exits when the connection drops or Close is called.
func (c *Client) readLoop() {
	defer close(c.recv)
	for {
		_, data, err := c.conn.readMessage()
		if err != nil {
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

// pingLoop sends keepalive pings at the configured interval.
// Exits when Close is called.
func (c *Client) pingLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = c.conn.ping(time.Now().Add(c.writeTimeout))
		case <-c.closed:
			return
		}
	}
}

// ── Decoding ───────────────────────────────────────────────────────────────────

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
