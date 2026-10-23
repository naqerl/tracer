package health

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"tracer/libs/types"
)

// HTTPChecker probes one URL and reports down only when every attempt fails
// or returns an unexpected status.
type HTTPChecker struct {
	url            string
	method         string
	timeout        time.Duration
	attempts       int
	retryInterval  time.Duration
	expectedStatus map[int]struct{}
}

type Result struct {
	Time   time.Time
	Status types.HealthStatus
}

func NewHTTPChecker(cfg HTTPConfig) *HTTPChecker {
	return &HTTPChecker{
		url:            cfg.URL,
		method:         cfg.Method,
		timeout:        cfg.Timeout,
		attempts:       cfg.Attempts,
		retryInterval:  cfg.RetryInterval,
		expectedStatus: cfg.ExpectedStatus,
	}
}

func (c *HTTPChecker) Check(ctx context.Context) Result {
	startedAt := time.Now().UTC()
	attemptErrors := make([]error, 0, c.attempts)

loop:
	for attempt := 1; attempt <= c.attempts; attempt++ {
		status, err := c.attempt(ctx)
		if err == nil {
			if c.isExpected(status) {
				return Result{Time: startedAt, Status: types.HealthStatusUp}
			}
			err = fmt.Errorf("unexpected status %d", status)
		}
		attemptErrors = append(attemptErrors, fmt.Errorf("attempt %d: %w", attempt, err))

		if attempt == c.attempts {
			break
		}
		select {
		case <-ctx.Done():
			attemptErrors = append(attemptErrors, ctx.Err())
			break loop
		case <-time.After(c.retryInterval):
		}
	}

	if ctx.Err() == nil {
		slog.Warn("health check failed",
			"url", c.url,
			"attempts", c.attempts,
			"error", errors.Join(attemptErrors...),
		)
	}
	return Result{Time: startedAt, Status: types.HealthStatusDown}
}

// attempt performs one request on its own connection. HTTP/1.1 keeps
// DisableKeepAlives deterministic: each attempt is a new TCP and TLS handshake.
func (c *HTTPChecker) attempt(ctx context.Context) (int, error) {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: c.timeout}).DialContext,
		TLSHandshakeTimeout:   c.timeout,
		ResponseHeaderTimeout: c.timeout,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     false,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	defer transport.CloseIdleConnections()

	client := &http.Client{Timeout: c.timeout, Transport: transport}
	req, err := http.NewRequestWithContext(ctx, c.method, c.url, nil)
	if err != nil {
		return 0, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	// The body is not part of the verdict, and the connection is discarded
	// after this attempt.
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Debug("could not close health check response body", "url", c.url, "error", cerr)
		}
	}()

	return resp.StatusCode, nil
}

func (c *HTTPChecker) isExpected(status int) bool {
	if len(c.expectedStatus) == 0 {
		return status >= 200 && status < 300
	}
	_, ok := c.expectedStatus[status]
	return ok
}
