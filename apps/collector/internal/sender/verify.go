package sender

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"tracer/libs/types"
)

const (
	verifyDialTimeout  = 10 * time.Second
	verifyWriteTimeout = 10 * time.Second
	verifyReadWait     = 2 * time.Second // grace period to catch an immediate close
)

// Verify checks that the collector can reach the panel, authenticate with the
// configured API key, and complete registration. It performs a single
// one-shot connection (no reconnection) and reports the first failure with
// actionable context.
//
// Used by the "verify" subcommand to validate collector configuration.
func Verify(ctx context.Context, spec Spec, appNames []string) error {
	if spec.APIKey == "" {
		return errors.New("api_key is not set, run: sudo tracer-collector set api-key <collector-api-key>")
	}
	if spec.PanelHost == "" {
		return errors.New("panel_host is not set, run: sudo tracer-collector set panel-host <panel-host>:8080")
	}
	if spec.Name == "" {
		return errors.New("collector name is not set, check the sender.name config")
	}

	url, err := panelWSURL(spec.PanelHost)
	if err != nil {
		return fmt.Errorf("could not build panel URL: %w", err)
	}

	headers := http.Header{}
	headers.Set("X-API-Key", spec.APIKey)

	// Dial directly (instead of wsx.New) so a rejected handshake keeps the
	// HTTP response, letting us tell an invalid key apart from a host that is
	// not a tracer panel.
	dialer := websocket.Dialer{HandshakeTimeout: verifyDialTimeout}
	conn, resp, err := dialer.DialContext(ctx, url, headers)
	if err != nil {
		switch {
		case resp != nil && resp.StatusCode == http.StatusUnauthorized:
			return errors.New("panel rejected the API key (HTTP 401), run: sudo tracer-collector set api-key <collector-api-key>")
		case errors.Is(err, websocket.ErrBadHandshake):
			status := "unknown"
			if resp != nil {
				status = resp.Status
			}
			return fmt.Errorf("panel rejected the WebSocket handshake (HTTP %s), check that %q is the tracer panel host", status, spec.PanelHost)
		default:
			return fmt.Errorf("could not connect to panel at %s: %w", url, err)
		}
	}
	defer func() {
		if err := conn.Close(); err != nil {
			slog.Warn("verify: failed to close connection", "error", err)
		}
	}()

	// Unblock the read/write below when the caller cancels (e.g. Ctrl-C).
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	// Complete registration, mirroring what the sender does on connect.
	conn.SetWriteDeadline(time.Now().Add(verifyWriteTimeout))
	if err := conn.WriteJSON(types.RegisterEvent{
		Type:      types.EventTypeRegister,
		Collector: spec.Name,
		Apps:      appNames,
	}); err != nil {
		return fmt.Errorf("could not send registration to panel: %w", err)
	}

	// The panel sends nothing back; a read deadline that expires without a
	// close frame means the connection is still alive after registration.
	conn.SetReadDeadline(time.Now().Add(verifyReadWait))
	for {
		_, _, err := conn.ReadMessage()
		if err == nil {
			continue // any panel message is fine, keep waiting for the deadline
		}
		if ctx.Err() != nil {
			return fmt.Errorf("verification cancelled: %w", ctx.Err())
		}
		var netErr net.Error
		if !errors.As(err, &netErr) {
			return fmt.Errorf("could not keep the connection open after registration: %w", err)
		}
		if netErr.Timeout() {
			return nil
		}
		return fmt.Errorf("could not keep the connection open after registration: %w", err)
	}
}
