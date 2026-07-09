package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"tracer/libs/types"
	"tracer/libs/wsx"
)

// Spec holds the WebSocket connection configuration.
type Spec struct {
	Name      string `yaml:"name"`
	PanelHost string `yaml:"panel_host"`
	APIKey    string `yaml:"api_key"`
}

// Service handles WebSocket connection to panel.
type Service struct {
	spec     Spec
	appNames []string
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
// It blocks until the context is cancelled.
func (s *Service) Start(ctx context.Context) error {
	// Prepare headers with API key for authentication.
	headers := http.Header{}
	headers.Set("X-API-Key", s.spec.APIKey)

	client := wsx.NewClient(wsx.Config{
		URL:     s.spec.PanelHost,
		Headers: headers,
		// Use defaults for HandshakeTimeout, WriteTimeout, PingInterval, etc.
		OnConnect: func(ctx context.Context, conn *wsx.Conn) error {
			slog.Info("Connected to panel, sending registration",
				"host", s.spec.PanelHost,
				"collector", s.spec.Name,
			)
			return s.sendRegistration(conn)
		},
	})

	// Forward events from the events channel to the WebSocket connection.
	// This goroutine runs for the lifetime of ctx; client.SendJSON is safe for
	// concurrent use and returns an error if the connection is down.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-s.events:
				if !ok {
					return
				}
				if err := s.sendEvent(client, evt); err != nil {
					slog.Warn("wsx: failed to send event", "error", err)
				}
			}
		}
	}()

	err := client.Start(ctx)
	wg.Wait()
	return err
}

func (s *Service) sendRegistration(conn *wsx.Conn) error {
	msg := types.RegisterMessage{
		Type:      types.EventTypeRegister,
		Collector: s.spec.Name,
		Apps:      s.appNames,
	}
	return conn.SendJSON(msg)
}

func (s *Service) sendEvent(client *wsx.Client, evt types.Event) error {
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
	return client.SendJSON(msg)
}
