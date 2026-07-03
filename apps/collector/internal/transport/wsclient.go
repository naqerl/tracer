package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/types"
)

// UpdateFn is called when the collector receives an update message from the panel.
// It replaces s.conn and restarts via systemctl.
var UpdateFn func()

const (
	writeTimeout     = 10 * time.Second
	reconnectDelay   = 5 * time.Second
	pingInterval     = 30 * time.Second
	pongWait         = 60 * time.Second
	maxReconnectWait = 5 * time.Minute
)

// Service handles WebSocket connection to panel.
type Service struct {
	spec     Spec
	appNames []string
	conn     *websocket.Conn
	events   <-chan types.Event
}

// New creates a WebSocket client service.
func New(spec Spec, appNames []string, events <-chan types.Event) Service {
	return Service{
		spec:     spec,
		appNames: appNames,
		events:   events,
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

		s.close()
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

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	s.conn = conn
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

	// Channel for incoming panel messages
	msgCh := make(chan []byte, 8)
	go s.readMessages(msgCh)

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-msgCh:
			s.handlePanelMessage(msg)
		case evt := <-s.events:
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

// readMessages reads incoming WebSocket messages and sends them to the channel.
func (s *Service) readMessages(ch chan<- []byte) {
	for {
		_, message, err := s.conn.ReadMessage()
		if err != nil {
			close(ch)
			return
		}
		// Ping/pong messages are handled by the connection's pong handler,
		// so any message here is a panel command.
		select {
		case ch <- message:
		default:
			slog.Warn("Dropping panel message (channel full)")
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
		go func() {
			if UpdateFn != nil {
				UpdateFn()
			} else {
				slog.Warn("No update function configured, running default update")
				s.handleUpdate()
			}
		}()
	default:
		slog.Debug("Received unknown message type from panel", "type", hdr.Type)
	}
}

// handleUpdate runs the collector self-update and restarts.
func (s *Service) handleUpdate() {
	slog.Info("Running collector self-update...")

	// The update will download source, build, and replace the binary
	if err := runUpdateBinary(); err != nil {
		slog.Error("Collector update failed", "error", err)
		return
	}

	slog.Info("Collector binary updated, restarting service...")

	// Restart via systemd — this kills the current process
	if err := exec.Command("systemctl", "restart", "tracer-collector").Run(); err != nil {
		slog.Error("Failed to restart tracer-collector", "error", err)
	}
}

// runUpdateBinary executes the tracer-collector update command.
func runUpdateBinary() error {
	cmd := exec.Command("tracer-collector", "update")
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
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
