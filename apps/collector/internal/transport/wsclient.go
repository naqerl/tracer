package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/shared/updatex"
	"tracer/libs/shared/wsconn"
	"tracer/libs/types"
)

const (
	reconnectDelay   = 5 * time.Second
	maxReconnectWait = 5 * time.Minute
)

// Service handles WebSocket connection to panel.
type Service struct {
	spec     Spec
	appNames []string
	peer     *wsconn.Peer
	peerMu   sync.Mutex
	events   <-chan types.Event
	updateFn func() *updatex.Report
}

// New creates a WebSocket client service.
// updateFn is called when an update command is received from the panel.
func New(spec Spec, appNames []string, events <-chan types.Event, updateFn func() *updatex.Report) *Service {
	return &Service{
		spec:     spec,
		appNames: appNames,
		events:   events,
		updateFn: updateFn,
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

		s.peerMu.Lock()
		s.peer.Close()
		s.peer = nil
		s.peerMu.Unlock()
	}
}

func (s *Service) connect() error {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()

	if s.peer != nil {
		s.peer.Close()
		s.peer = nil
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

	ws := wsconn.New(conn)
	s.peer = wsconn.NewPeer(ws, s.handleEvent)
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
	for {
		select {
		case <-ctx.Done():
			return nil
		case evt := <-s.events:
			if err := s.sendEvent(evt); err != nil {
				return err
			}
		case <-s.peer.Closed():
			return fmt.Errorf("connection closed")
		}
	}
}

func (s *Service) sendRegistration() {
	msg := types.RegisterEvent{
		Type:      types.EventTypeRegister,
		Collector: s.spec.Name,
		Apps:      s.appNames,
	}
	s.peer.Send(msg) // nolint: errcheck — registration is best-effort
}

func (s *Service) sendEvent(evt types.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("could not marshal payload: %w", err)
	}

	msg := types.Envelope{
		Type:      evt.EventType(),
		Time:      time.Now().UTC(),
		Collector: s.spec.Name,
		Data:      payload,
	}
	return s.peer.Send(msg)
}

// handleEvent processes an event received from the panel.
// For UpdateRequestedEvent it starts a self-update and forwards progress
// events back through the WebSocket.
func (s *Service) handleEvent(evt types.Event) {
	switch msg := evt.(type) {
	case types.UpdateRequestedEvent:
		slog.Info("Received update command from panel", "version", msg.Version)
		go func() {
			report := s.updateFn()
			for pe := range report.Events {
				s.SendProgress(pe.Stage, pe.Status, pe.Message)
			}
			if err := report.Wait(); err != nil {
				slog.Error("Collector update failed", "error", err)
			}
		}()
	default:
		slog.Debug("Received unknown message type from panel", "type", evt.EventType())
	}
}

// SendProgress sends an update progress event to the panel.
func (s *Service) SendProgress(stage string, status types.ProgressStatus, message string) {
	s.peerMu.Lock()
	peer := s.peer
	s.peerMu.Unlock()
	if peer == nil {
		return
	}
	evt := types.UpdateProgressEvent{
		Type:    types.EventTypeUpdateProgress,
		Time:    time.Now().UTC(),
		Entity:  s.spec.Name,
		Stage:   stage,
		Status:  status,
		Message: message,
	}
	// Send as envelope so the panel knows which collector sent it.
	payload, err := json.Marshal(evt)
	if err != nil {
		slog.Error("Failed to marshal progress event", "error", err)
		return
	}
	msg := types.Envelope{
		Type:      evt.EventType(),
		Time:      evt.Time,
		Collector: s.spec.Name,
		Data:      payload,
	}
	if err := peer.Send(msg); err != nil {
		slog.Debug("Failed to send progress event", "error", err)
	}
}
