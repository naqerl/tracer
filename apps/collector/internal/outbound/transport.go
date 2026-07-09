package outbound

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
	client Client
	events <-chan types.Event
	spec   Spec
}

// New creates a Service. The client automatically reconnects on failure.
//
// The factory dials the panel, authenticates, and sends a RegisterEvent
// before returning. If registration fails, the entire factory call fails
// and the reconnecting wrapper retries.
func New(spec Spec, appNames []string, events <-chan types.Event) Service {
	headers := http.Header{}
	headers.Set("X-API-Key", spec.APIKey)

	factory := func() (Client, error) {
		url := "ws://" + spec.PanelHost + "/ws"
		c, err := wsx.New(url,
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
		return c, nil
	}

	return Service{
		client: newReconnecting(factory),
		events: events,
		spec:   spec,
	}
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
				return nil
			}
			if err := s.sendEvent(evt); err != nil {
				slog.Warn("failed to send event", "error", err)
			}
		}
	}
}

func (s *Service) sendEvent(evt types.Event) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	env := types.Envelope{
		Type:      evt.EventType(),
		TS:        time.Now().UTC(),
		Collector: s.spec.Name,
		Data:      payload,
	}
	return s.client.Send(env)
}
