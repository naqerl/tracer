package health

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"tracer/libs/types"
)

type HTTPConfig struct {
	URL            string
	Method         string
	Timeout        time.Duration
	Attempts       int
	RetryInterval  time.Duration
	ExpectedStatus map[int]struct{}
}

func (HTTPConfig) Default() HTTPConfig {
	return HTTPConfig{
		Method:         "GET",
		Timeout:        5 * time.Second,
		Attempts:       3,
		RetryInterval:  time.Second,
		ExpectedStatus: map[int]struct{}{200: {}},
	}
}

func specInterval(s types.HealthCheckSpec) (time.Duration, error) {
	raw := strings.TrimSpace(s.Params["interval"])
	if raw == "" {
		return 30 * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid params.interval: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid params.interval: must be positive")
	}
	return d, nil
}

func specHTTPConfig(s types.HealthCheckSpec) (HTTPConfig, error) {
	url, err := getParam(s, "url")
	if err != nil {
		return HTTPConfig{}, err
	}

	cfg := HTTPConfig{}.Default()

	method := strings.ToUpper(strings.TrimSpace(s.Params["method"]))
	if method == "" {
		method = cfg.Method
	}

	timeout := cfg.Timeout
	if raw := strings.TrimSpace(s.Params["timeout"]); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return HTTPConfig{}, fmt.Errorf("invalid params.timeout: %w", err)
		}
		if d <= 0 {
			return HTTPConfig{}, fmt.Errorf("invalid params.timeout: must be positive")
		}
		timeout = d
	}

	attempts := cfg.Attempts
	if raw := strings.TrimSpace(s.Params["attempts"]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return HTTPConfig{}, fmt.Errorf("invalid params.attempts: %w", err)
		}
		if n < 1 || n > 10 {
			return HTTPConfig{}, fmt.Errorf("invalid params.attempts: must be between 1 and 10")
		}
		attempts = n
	}

	retryInterval := cfg.RetryInterval
	if raw := strings.TrimSpace(s.Params["retry_interval"]); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return HTTPConfig{}, fmt.Errorf("invalid params.retry_interval: %w", err)
		}
		if d < 0 {
			return HTTPConfig{}, fmt.Errorf("invalid params.retry_interval: must not be negative")
		}
		retryInterval = d
	}

	expected := cfg.ExpectedStatus
	if raw := strings.TrimSpace(s.Params["expected_status"]); raw != "" {
		expected = map[int]struct{}{}
		for _, part := range strings.Split(raw, ",") {
			code, err := strconv.Atoi(strings.TrimSpace(part))
			if err != nil {
				return HTTPConfig{}, fmt.Errorf("invalid params.expected_status: %w", err)
			}
			expected[code] = struct{}{}
		}
	}

	return HTTPConfig{
		URL:            url,
		Method:         method,
		Timeout:        timeout,
		Attempts:       attempts,
		RetryInterval:  retryInterval,
		ExpectedStatus: expected,
	}, nil
}

func getParam(spec types.HealthCheckSpec, key string) (string, error) {
	if spec.Params == nil {
		return "", fmt.Errorf("missing params.%s", key)
	}
	if v := strings.TrimSpace(spec.Params[key]); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("missing params.%s", key)
}
