package wsx

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
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

// Config holds client configurations for a reconnecting WebSocket client.
type Config struct {
	// URL is the WebSocket server endpoint (e.g. "ws://host/ws").
	URL string

	// Headers are sent during the initial handshake.
	Headers http.Header

	// HandshakeTimeout is the maximum time to wait for the connection to establish.
	HandshakeTimeout time.Duration

	// WriteTimeout is the deadline for each write operation.
	WriteTimeout time.Duration

	// PingInterval is how often the client sends ping frames.
	PingInterval time.Duration

	// PongWait is how long the client waits for a pong before considering the
	// connection dead (also used as the read deadline).
	PongWait time.Duration

	// ReconnectDelay is the initial delay before reconnecting after a failure.
	ReconnectDelay time.Duration

	// MaxReconnectWait is the maximum backoff delay between reconnect attempts.
	MaxReconnectWait time.Duration

	// ReadLimit sets the maximum message size in bytes. 0 means no limit.
	ReadLimit int64

	// OnConnect is called after a successful connection and handshake.
	// If it returns an error, the connection is closed and a reconnect is attempted.
	OnConnect func(ctx context.Context, conn *Conn) error

	// OnDisconnect is called when the connection is lost (after cleanup).
	OnDisconnect func()

	// ReadHandler is called for every incoming message received on the connection.
	// If nil, incoming messages are silently discarded (but the read loop still
	// runs to process pong responses).
	ReadHandler func(messageType int, data []byte)
}

// setDefaults fills in zero-valued fields with sensible defaults.
func (c *Config) setDefaults() {
	if c.HandshakeTimeout <= 0 {
		c.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = DefaultWriteTimeout
	}
	if c.PingInterval <= 0 {
		c.PingInterval = DefaultPingInterval
	}
	if c.PongWait <= 0 {
		c.PongWait = DefaultPongWait
	}
	if c.ReconnectDelay <= 0 {
		c.ReconnectDelay = DefaultReconnectDelay
	}
	if c.MaxReconnectWait <= 0 {
		c.MaxReconnectWait = DefaultMaxReconnectWait
	}
	if c.ReadLimit <= 0 {
		c.ReadLimit = DefaultReadLimit
	}
}

// Client maintains a reconnecting WebSocket connection with keepalive pings.
type Client struct {
	config   Config
	conn     *Conn
	connMu   sync.RWMutex
	dialer   websocket.Dialer
	closed   bool
	closedMu sync.Mutex
}

// NewClient creates a new WebSocket client with the given configuration.
// The client does not connect until Start is called.
func NewClient(config Config) *Client {
	config.setDefaults()
	return &Client{
		config: config,
		dialer: websocket.Dialer{
			HandshakeTimeout: config.HandshakeTimeout,
		},
	}
}

// Conn returns the current active connection, or nil if not connected.
func (c *Client) Conn() *Conn {
	c.connMu.RLock()
	defer c.connMu.RUnlock()
	return c.conn
}

// Start begins the connection loop. It blocks until the context is cancelled.
// On successful connection, the OnConnect callback is invoked. If the connection
// drops, it automatically reconnects with exponential backoff.
func (c *Client) Start(ctx context.Context) error {
	reconnectWait := c.config.ReconnectDelay

	for {
		// Check if we should stop before attempting a connection.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err := c.connect(ctx); err != nil {
			slog.Error("wsx: failed to connect", "url", c.config.URL, "error", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(reconnectWait):
			}
			reconnectWait = time.Duration(math.Min(
				float64(reconnectWait)*2,
				float64(c.config.MaxReconnectWait),
			))
			continue
		}

		reconnectWait = c.config.ReconnectDelay
		slog.Info("wsx: connected", "url", c.config.URL)

		// OnConnect hook
		if c.config.OnConnect != nil {
			if err := c.config.OnConnect(ctx, c.conn); err != nil {
				slog.Error("wsx: OnConnect failed, reconnecting", "error", err)
				c.close()
				continue
			}
		}

		// Run the read/ping loop until error or context cancellation.
		// run() only returns on error or context cancellation.
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

func (c *Client) connect(ctx context.Context) error {
	conn, _, err := c.dialer.DialContext(ctx, c.config.URL, c.config.Headers)
	if err != nil {
		return err
	}

	wsConn := NewConn(conn, c.config.WriteTimeout)
	wsConn.SetReadLimit(c.config.ReadLimit)
	wsConn.SetReadDeadline(time.Now().Add(c.config.PongWait))
	wsConn.SetPongHandler(func(string) error {
		wsConn.SetReadDeadline(time.Now().Add(c.config.PongWait))
		return nil
	})

	c.connMu.Lock()
	c.conn = wsConn
	c.connMu.Unlock()
	return nil
}

func (c *Client) run(ctx context.Context) error {
	pingTicker := time.NewTicker(c.config.PingInterval)
	defer pingTicker.Stop()

	// Snapshot the connection; c.conn is stable while run() owns it.
	// close() only mutates c.conn after run() returns.
	conn := c.conn

	// Read errors are communicated via this channel.
	readErr := make(chan error, 1)
	go c.readLoop(readErr, conn)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readErr:
			return err
		case <-pingTicker.C:
			if err := conn.Ping(time.Now().Add(c.config.WriteTimeout)); err != nil {
				return err
			}
		}
	}
}

// readLoop reads messages from the connection until an error occurs.
// If a ReadHandler is configured, it is called for each message.
func (c *Client) readLoop(errCh chan<- error, conn *Conn) {
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			errCh <- err
			return
		}
		if c.config.ReadHandler != nil {
			c.config.ReadHandler(msgType, data)
		}
	}
}

// SendJSON sends a JSON-encoded message on the current connection.
// It is safe for concurrent use.
func (c *Client) SendJSON(v any) error {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()
	if conn == nil {
		return websocket.ErrCloseSent
	}
	return conn.SendJSON(v)
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
		c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
	if c.config.OnDisconnect != nil {
		c.config.OnDisconnect()
	}
}
