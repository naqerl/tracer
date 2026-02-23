package wsclient

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ai-shift/tracer/apps/collector/internal/events"
	"github.com/ai-shift/tracer/libs/types"
)

const (
	writeTimeout     = 10 * time.Second
	reconnectDelay   = 5 * time.Second
	pingInterval     = 30 * time.Second
	pongWait         = 60 * time.Second
	maxReconnectWait = 5 * time.Minute
)

// Service handles WebSocket connection to panel.
type Service struct {
	endpoint      string
	apiKey        string
	collectorName string
	appNames      []string
	bus           *events.Bus
	conn          *websocket.Conn
}

// New creates a WebSocket client service.
func New(endpoint, apiKey, collectorName string, appNames []string, bus *events.Bus) *Service {
	return &Service{
		endpoint:      endpoint,
		apiKey:        apiKey,
		collectorName: collectorName,
		appNames:      appNames,
		bus:           bus,
	}
}

// Start begins the connection loop and event processing.
func (s *Service) Start(ctx context.Context) {
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
			reconnectWait = min(reconnectWait*2, maxReconnectWait)
			continue
		}

		reconnectWait = reconnectDelay
		slog.Info("Connected to panel", "endpoint", s.endpoint)

		s.sendRegistration()

		if err := s.run(ctx); err != nil {
			slog.Warn("Connection lost", "error", err)
		}

		s.close()
	}
}

func (s *Service) connect() error {
	if s.conn != nil {
		s.conn.Close()
	}

	headers := http.Header{}
	if s.apiKey != "" {
		headers.Set("X-API-Key", s.apiKey)
	}

	conn, _, err := websocket.DefaultDialer.Dial(s.endpoint, headers)
	if err != nil {
		return err
	}

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	s.conn = conn
	return nil
}

func (s *Service) run(ctx context.Context) error {
	ch := s.bus.Subscribe()
	defer func() {
		// Note: bus channels are not closed on unsubscribe in current implementation
	}()

	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	// Start read loop for pongs
	pongDone := make(chan struct{})
	go s.readLoop(pongDone)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-pongDone:
			return websocket.ErrBadHandshake
		case evt := <-ch:
			if err := s.sendEvent(evt); err != nil {
				return err
			}
		case <-pingTicker.C:
			if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)); err != nil {
				return err
			}
		}
	}
}

func (s *Service) readLoop(done chan<- struct{}) {
	defer close(done)
	for {
		if _, _, err := s.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (s *Service) sendRegistration() {
	msg := types.LogMessage{
		Type:          types.MessageTypeRegister,
		CollectorName: s.collectorName,
		Apps:          s.appNames,
	}
	s.sendJSON(msg)
}

func (s *Service) sendEvent(evt types.Event) error {
	// Convert to LogMessage format expected by panel
	msgType := types.MessageTypeLog
	switch evt.EventType() {
	case "startup":
		msgType = types.MessageTypeStartup
	case "stats":
		msgType = types.MessageTypeStats
	}

	msg := types.LogMessage{
		Type:          msgType,
		CollectorName: s.collectorName,
		AppName:       evt.AppName(),
	}

	// Set the appropriate payload based on event type
	switch e := evt.(type) {
	case types.LogEvent:
		msg.Entry = e.Entry
	case types.StartupEvent:
		// Create a log entry for startup events
		msg.Entry = types.LogEntry{
			Timestamp: e.Timestamp(),
			Level:     types.LevelInfo,
			Message:   "STARTUP: " + e.Message,
		}
	case types.StatsEvent:
		msg.Stats = &e.Data
	case types.ErrorEvent:
		// Error events are logged as errors with the error details
		msg.Entry = types.LogEntry{
			Timestamp: e.Timestamp(),
			Level:     types.LevelError,
			Message:   fmt.Sprintf("SOURCE_ERROR[%s]: %s (source: %s)", e.ErrType, e.Message, e.Source),
		}
	}

	return s.sendJSON(msg)
}

func (s *Service) sendJSON(v any) error {
	s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return s.conn.WriteJSON(v)
}

func (s *Service) close() {
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
