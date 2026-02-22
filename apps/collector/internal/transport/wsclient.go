package wsclient

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ai-shift/tracer/apps/collector/internal/stats"
	"github.com/ai-shift/tracer/apps/collector/internal/watch"
	"github.com/ai-shift/tracer/libs/types"
)

const (
	writeTimeout     = 10 * time.Second
	reconnectDelay   = 5 * time.Second
	pingInterval     = 30 * time.Second
	pongWait         = 60 * time.Second // Time allowed to read the next pong message from server
	maxReconnectWait = 5 * time.Minute
)



type Service struct {
	endpoint      string
	collectorName string
	appNames      []string
	logChan       <-chan watchdog.AppLogEntry

	// System stats
	statsCollector *sysstats.Collector
	statsInterval  time.Duration

	// Alert thresholds (sent to panel)
	cpuAlert  float64
	memAlert  float64
	diskAlert float64

	conn   *websocket.Conn
	connMu sync.Mutex
}

func New(endpoint, collectorName string, appNames []string, logChan <-chan watchdog.AppLogEntry) *Service {
	return &Service{
		endpoint:      endpoint,
		collectorName: collectorName,
		appNames:      appNames,
		logChan:       logChan,
	}
}

// WithStats configures the service to send system stats at the given interval
func (s *Service) WithStats(collector *sysstats.Collector, interval time.Duration) *Service {
	s.statsCollector = collector
	s.statsInterval = interval
	return s
}

// WithAlerts configures alert thresholds for resource usage (sent to panel)
func (s *Service) WithAlerts(cpuAlert, memAlert, diskAlert float64) *Service {
	s.cpuAlert = cpuAlert
	s.memAlert = memAlert
	s.diskAlert = diskAlert
	return s
}

func (s *Service) Start(ctx context.Context) {
	go s.maintainConnection(ctx)

	// Start stats loop if configured
	if s.statsCollector != nil && s.statsInterval > 0 {
		go s.statsLoop(ctx)
	}

	s.processLogs(ctx)
}

func (s *Service) maintainConnection(ctx context.Context) {
	reconnectWait := reconnectDelay

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := s.connect(); err != nil {
			slog.Error("Failed to connect to panel", "endpoint", s.endpoint, "error", err)
			time.Sleep(reconnectWait)
			// Exponential backoff
			reconnectWait = min(reconnectWait*2, maxReconnectWait)
			continue
		}

		// Reset backoff on successful connection
		reconnectWait = reconnectDelay
		slog.Info("Connected to panel", "endpoint", s.endpoint)

		// Send registration message
		s.sendRegistration()

		// Keep connection alive with pings
		s.pingLoop(ctx)
	}
}

func (s *Service) connect() error {
	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.conn != nil {
		s.conn.Close()
	}

	conn, _, err := websocket.DefaultDialer.Dial(s.endpoint, nil)
	if err != nil {
		return err
	}

	// Set up pong handler to reset read deadline when server responds to our pings
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	s.conn = conn
	return nil
}

func (s *Service) pingLoop(ctx context.Context) {
	// Channel to signal connection is dead (from read loop)
	connDead := make(chan struct{})

	// Start read loop to handle incoming messages and keep connection alive
	// This is essential: Gorilla WebSocket only processes control frames (ping/pong)
	// when ReadMessage is called. Without this, server pings are never answered.
	go s.readLoop(connDead)

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-connDead:
			// Connection died, exit to trigger reconnect
			return
		case <-ticker.C:
			s.connMu.Lock()
			if s.conn == nil {
				s.connMu.Unlock()
				return
			}
			err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout))
			s.connMu.Unlock()

			if err != nil {
				slog.Warn("Ping failed, reconnecting", "error", err)
				return
			}
		}
	}
}

// readLoop continuously reads from the WebSocket connection.
// This serves two purposes:
// 1. Allows Gorilla WebSocket to process control frames (ping from server -> automatic pong response)
// 2. Resets the read deadline via the pong handler when server responds to our pings
func (s *Service) readLoop(connDead chan<- struct{}) {
	defer close(connDead)

	for {
		s.connMu.Lock()
		conn := s.conn
		s.connMu.Unlock()

		if conn == nil {
			return
		}

		// ReadMessage will block until a message arrives or deadline expires.
		// It also processes control frames (ping/pong) automatically.
		// We don't expect any actual messages from the server, so we discard them.
		_, _, err := conn.ReadMessage()
		if err != nil {
			// Connection closed or error - signal to ping loop
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Debug("Read loop ended", "error", err)
			}
			return
		}
	}
}

func (s *Service) processLogs(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.close()
			return
		case entry, ok := <-s.logChan:
			if !ok {
				s.close()
				return
			}
			s.sendLog(entry)
		}
	}
}

func (s *Service) sendRegistration() {
	msg := types.LogMessage{
		Type:          "register",
		Apps:          s.appNames,
		CollectorName: s.collectorName,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Failed to marshal registration", "error", err)
		return
	}

	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.conn == nil {
		return
	}

	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		slog.Warn("Failed to send registration", "error", err)
	}
}

func (s *Service) sendLog(appLog watchdog.AppLogEntry) {
	// Determine message type based on whether this is a startup entry
	msgType := "log"
	if appLog.IsStartup {
		msgType = "startup"
	}

	msg := types.LogMessage{
		Type:          msgType,
		AppName:       appLog.AppName,
		CollectorName: s.collectorName,
		Entry:         appLog.Entry,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Failed to marshal log entry", "error", err)
		return
	}

	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.conn == nil {
		slog.Warn("No connection, dropping log entry")
		return
	}

	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		slog.Warn("Failed to send log entry", "error", err)
		s.conn.Close()
		s.conn = nil
	}
}

func (s *Service) close() {
	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.conn != nil {
		s.conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(writeTimeout),
		)
		s.conn.Close()
		s.conn = nil
	}
}

// statsLoop periodically collects and sends system statistics
func (s *Service) statsLoop(ctx context.Context) {
	ticker := time.NewTicker(s.statsInterval)
	defer ticker.Stop()

	// Send initial stats immediately
	s.collectAndSendStats()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.collectAndSendStats()
		}
	}
}

func (s *Service) collectAndSendStats() {
	stats, err := s.statsCollector.Collect()
	if err != nil {
		slog.Error("Failed to collect system stats", "error", err)
		return
	}

	s.sendStats(stats)
}

func (s *Service) sendStats(stats *sysstats.Stats) {
	msg := types.LogMessage{
		Type:          "stats",
		CollectorName: s.collectorName,
		Stats:         stats,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		slog.Error("Failed to marshal stats", "error", err)
		return
	}

	s.connMu.Lock()
	defer s.connMu.Unlock()

	if s.conn == nil {
		return // No connection, skip this cycle
	}

	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		slog.Warn("Failed to send stats", "error", err)
		// Don't close connection on stats failure, let ping handle it
	}

	slog.Debug("Sent system stats", "collector", s.collectorName, "cpu", stats.CPUUsagePercent)
}
