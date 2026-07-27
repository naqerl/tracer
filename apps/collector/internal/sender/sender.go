package sender

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tracer/libs/types"
	"tracer/libs/wsx"
)

// Spec holds the WebSocket connection configuration for the collector.
type Spec struct {
	Name      string `yaml:"name"`
	PanelHost string `yaml:"panel_host"`
	APIKey    string `yaml:"api_key"`
}

// Service forwards events from a channel to the panel via a reconnecting
// WebSocket client. Registration is sent automatically on each connection.
type Service struct {
	client      Client
	events      <-chan types.Event
	spec        Spec
	PostConnect func(client Client) // called after each successful connection (after RegisterEvent)
}

// New creates a Service. The client automatically reconnects on failure.
//
// The factory dials the panel, authenticates, and sends a RegisterEvent
// before returning. If registration fails, the entire factory call fails
// and the reconnecting wrapper retries.
//
// If Service.PostConnect is set, it is called after each successful connection
// (after RegisterEvent). Callers can use this to send HelloReport or other
// post-connect messages.
func New(spec Spec, appNames []string, events <-chan types.Event) *Service {
	s := &Service{
		events: events,
		spec:   spec,
	}

	headers := http.Header{}
	headers.Set("X-API-Key", spec.APIKey)

	factory := func(ctx context.Context) (Client, error) {
		url, err := panelWSURL(spec.PanelHost)
		if err != nil {
			return nil, fmt.Errorf("build URL: %w", err)
		}
		c, err := wsx.New(ctx, url,
			wsx.WithHeaders(headers),
			wsx.WithWriteTimeout(10*time.Second),
		)
		if err != nil {
			return nil, fmt.Errorf("dial: %w", err)
		}
		if err := c.Send(types.RegisterEvent{
			Type:      types.EventTypeRegister,
			Collector: spec.Name,
			Apps:      appNames,
		}); err != nil {
			c.Close()
			return nil, fmt.Errorf("register: %w", err)
		}
		if s.PostConnect != nil {
			s.PostConnect(c)
		}
		return c, nil
	}

	s.client = newReconnecting(context.Background(), factory)
	return s
}

// Start forwards events from the events channel to the panel.
// It blocks until the context is cancelled or the events channel is closed.
func (s *Service) Start(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			s.client.Close()
			return ctx.Err()
		case evt, ok := <-s.events:
			if !ok {
				s.client.Close()
				return nil
			}
			env := types.Envelope{
				Type:      evt.EventType(),
				Time:      time.Now().UTC(),
				Collector: s.spec.Name,
				Data:      evt,
			}
			if err := s.client.Send(env); err != nil {
				slog.Warn("failed to send event", "error", err)
			}
		}
	}
}

// RunRecv reads incoming events from the WebSocket connection and dispatches
// them through the router. It blocks until the context is cancelled or the
// receive channel is closed.
func (s *Service) RunRecv(ctx context.Context, router *Router) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-s.client.Recv():
			if !ok {
				return
			}
			env, ok := evt.(types.Envelope)
			if !ok {
				slog.Debug("received non-envelope event, skipping", "type", evt.EventType())
				continue
			}
			router.Dispatch(env)
		}
	}
}

// Client returns the underlying Client, allowing the update agent to send
// status reports and other messages directly.
func (s *Service) Client() Client {
	return s.client
}

// panelWSURL builds a WebSocket URL from the panel_host config value.
// It accepts host:port, ws://host:port, wss://host:port, http://host:port,
// or https://host:port. The /ingest/ws path is appended.
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
