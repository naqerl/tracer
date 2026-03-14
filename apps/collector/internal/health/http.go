package health

import (
	"context"
	"net/http"
	"time"

	"tracer/libs/types"
)

type HTTPChecker struct {
	url            string
	method         string
	timeout        time.Duration
	expectedStatus map[int]struct{}
	client         *http.Client
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
		expectedStatus: cfg.ExpectedStatus,
		client:         &http.Client{Timeout: cfg.Timeout},
	}
}

func (c *HTTPChecker) Check(ctx context.Context) Result {
	result := Result{
		Time: time.Now().UTC(),
	}

	req, err := http.NewRequestWithContext(ctx, c.method, c.url, nil)
	if err != nil {
		result.Status = types.HealthStatusDown
		return result
	}

	resp, err := c.client.Do(req)
	if err != nil {
		result.Status = types.HealthStatusDown
		return result
	}
	defer resp.Body.Close()

	if c.isExpected(resp.StatusCode) {
		result.Status = types.HealthStatusUp
		return result
	}

	result.Status = types.HealthStatusDown
	return result
}

func (c *HTTPChecker) isExpected(status int) bool {
	if len(c.expectedStatus) == 0 {
		return status >= 200 && status < 300
	}
	_, ok := c.expectedStatus[status]
	return ok
}
