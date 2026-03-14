package health

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type Spec struct {
	Type   string            `yaml:"type"`
	Params map[string]string `yaml:"params"`
}

type HTTPConfig struct {
	URL            string
	Method         string
	Timeout        time.Duration
	ExpectedStatus map[int]struct{}
}

func (s Spec) interval() (time.Duration, error) {
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

func (s Spec) httpConfig() (HTTPConfig, error) {
	url, err := getParam(s, "url")
	if err != nil {
		return HTTPConfig{}, err
	}

	method := strings.ToUpper(strings.TrimSpace(s.Params["method"]))
	if method == "" {
		method = "GET"
	}

	timeout := 5 * time.Second
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

	expected := map[int]struct{}{}
	if raw := strings.TrimSpace(s.Params["expected_status"]); raw != "" {
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
		ExpectedStatus: expected,
	}, nil
}

func getParam(spec Spec, key string) (string, error) {
	if spec.Params == nil {
		return "", fmt.Errorf("missing params.%s", key)
	}
	if v := strings.TrimSpace(spec.Params[key]); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("missing params.%s", key)
}
