package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/shared/wsconn"
	"tracer/libs/types"
)

const (
	reconnectDelay   = 5 * time.Second
	pingInterval     = 30 * time.Second
	maxReconnectWait = 5 * time.Minute
)

// Service handles WebSocket connection to panel.
type Service struct {
	spec     Spec
	appNames []string
	conn     *wsconn.Conn
	events   <-chan types.Event
	commands chan<- types.Event
}

// New creates a WebSocket client service.
// events: incoming events from local producers to send to panel.
// commands: outgoing events triggered by panel messages (e.g. SelfUpdateEvent).
func New(spec Spec, appNames []string, events <-chan types.Event, commands chan<- types.Event) Service {
	return Service{
		spec:     spec,
		appNames: appNames,
		events:   events,
		commands: commands,
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
			slog.Error("Failed to connect to panel", "host", s.spec.PanelHost, "error", err)
			time.Sleep(reconnectWait)
			reconnectWait = min(reconnectWait*2, maxReconnectWait)
			continue
		}

		reconnectWait = reconnectDelay
		slog.Info("Connected to panel", "host", s.spec.PanelHost)

		s.sendRegistration()

		if err := s.run(ctx); err != nil {
			slog.Warn("Connection lost", "error", err)
		}

		s.conn.Close()
		s.conn = nil
	}
}

func (s *Service) connect() error {
	if s.conn != nil {
		s.conn.Close()
	}

	wsURL, err := panelWSURL(s.spec.PanelHost)
	if err != nil {
		return err
	}

	headers := http.Header{}
	headers.Set("X-API-Key", s.spec.APIKey)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		return err
	}

	s.conn = wsconn.New(conn)
	return nil
}

func panelWSURL(panelHost string) (string, error) {
	raw := strings.TrimSpace(panelHost)
	if raw == "" {
		return "", fmt.Errorf("panel_host is empty")
	}
	if !strings.Contains(raw, "://") {
		raw = "ws://" + raw
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid panel_host %q: %w", panelHost, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid panel_host %q: missing host", panelHost)
	}

	switch u.Scheme {
	case "ws", "wss":
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("invalid panel_host scheme %q (expected ws, wss, http, https, or host:port)", u.Scheme)
	}

	u.Path = "/ingest/ws"
	u.RawPath = ""
	return u.String(), nil
}

func (s *Service) run(ctx context.Context) error {
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-s.conn.Incoming():
			if !ok {
				return fmt.Errorf("connection closed")
			}
			s.handlePanelMessage(msg)
		case evt := <-s.events:
			if err := s.sendEvent(evt); err != nil {
				return err
			}
		case <-pingTicker.C:
			if err := s.conn.Ping(); err != nil {
				return err
			}
		}
	}
}

// handlePanelMessage processes an incoming message from the panel.
func (s *Service) handlePanelMessage(message []byte) {
	var hdr struct {
		Type types.EventType `json:"type"`
	}
	if err := json.Unmarshal(message, &hdr); err != nil {
		slog.Debug("Failed to parse panel message", "error", err)
		return
	}

	switch hdr.Type {
	case types.EventTypeUpdate:
		slog.Info("Received update command from panel")
		select {
		case s.commands <- types.SelfUpdateEvent{Time: time.Now()}:
		default:
			slog.Warn("Command channel full, dropping update command")
		}
	default:
		slog.Debug("Received unknown message type from panel", "type", hdr.Type)
	}
}

func (s *Service) sendRegistration() {
	msg := types.RegisterMessage{
		Type:      types.EventTypeRegister,
		Collector: s.spec.Name,
		Apps:      s.appNames,
	}
	s.sendJSON(msg)
}

func (s *Service) sendEvent(evt types.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("could not marshal payload: %w", err)
	}

	msg := types.Envelope{
		Type:      evt.EventType(),
		Timestamp: time.Now().UTC(),
		Collector: s.spec.Name,
		Data:      payload,
	}
	return s.sendJSON(msg)
}

// sendJSON marshals v and sends it through the connection's outgoing channel.
func (s *Service) sendJSON(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}

	select {
	case s.conn.Outgoing() <- data:
		return nil
	case <-s.conn.Closed():
		return fmt.Errorf("connection closed")
	}
}
