package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tracer/libs/types"
)

func TestSpecHTTPConfigParsesRetrySettings(t *testing.T) {
	cfg, err := specHTTPConfig(types.HealthCheckSpec{
		Type: "http",
		Params: map[string]string{
			"url":            "https://example.com/health",
			"timeout":        "15s",
			"attempts":       "1",
			"retry_interval": "0s",
		},
	})
	if err != nil {
		t.Fatalf("specHTTPConfig: %v", err)
	}
	if cfg.Timeout != 15*time.Second {
		t.Fatalf("timeout = %s, want 15s", cfg.Timeout)
	}
	if cfg.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", cfg.Attempts)
	}
	if cfg.RetryInterval != 0 {
		t.Fatalf("retry interval = %s, want 0s", cfg.RetryInterval)
	}
}

func TestSpecHTTPConfigRejectsRetrySettings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]string
	}{
		{name: "zero timeout", params: map[string]string{"timeout": "0s"}},
		{name: "negative timeout", params: map[string]string{"timeout": "-1s"}},
		{name: "bad timeout", params: map[string]string{"timeout": "soon"}},
		{name: "zero attempts", params: map[string]string{"attempts": "0"}},
		{name: "too many attempts", params: map[string]string{"attempts": "11"}},
		{name: "bad attempts", params: map[string]string{"attempts": "many"}},
		{name: "negative retry interval", params: map[string]string{"retry_interval": "-1s"}},
		{name: "bad retry interval", params: map[string]string{"retry_interval": "soon"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]string{"url": "https://example.com/health"}
			for key, value := range tc.params {
				params[key] = value
			}
			if _, err := specHTTPConfig(types.HealthCheckSpec{Type: "http", Params: params}); err == nil {
				t.Fatal("expected invalid params to be rejected")
			}
		})
	}
}

func TestSpecCommandConfigParsesTimeout(t *testing.T) {
	cfg, err := specCommandConfig(types.HealthCheckSpec{
		Type:   "command",
		Params: map[string]string{"command": "true", "timeout": "30s"},
	})
	if err != nil {
		t.Fatalf("specCommandConfig: %v", err)
	}
	if cfg.Timeout != 30*time.Second {
		t.Fatalf("timeout = %s, want 30s", cfg.Timeout)
	}

	if _, err := specCommandConfig(types.HealthCheckSpec{
		Type:   "command",
		Params: map[string]string{"command": "true", "timeout": "0s"},
	}); err == nil {
		t.Fatal("expected a zero command timeout to be rejected")
	}
}

func TestSpecHTTPTimeoutSeparatesSlowFromDefault(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(6 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer origin.Close()

	slowURL := origin.URL
	defaultCfg, err := specHTTPConfig(types.HealthCheckSpec{
		Type:   "http",
		Params: map[string]string{"url": slowURL, "attempts": "1", "retry_interval": "0s"},
	})
	if err != nil {
		t.Fatalf("specHTTPConfig default: %v", err)
	}
	if got := NewHTTPChecker(defaultCfg).Check(t.Context()).Status; got != types.HealthStatusDown {
		t.Fatalf("default timeout status = %q, want down", got)
	}

	slowCfg, err := specHTTPConfig(types.HealthCheckSpec{
		Type:   "http",
		Params: map[string]string{"url": slowURL, "timeout": "15s", "attempts": "1", "retry_interval": "0s"},
	})
	if err != nil {
		t.Fatalf("specHTTPConfig 15s: %v", err)
	}
	if got := NewHTTPChecker(slowCfg).Check(t.Context()).Status; got != types.HealthStatusUp {
		t.Fatalf("15s timeout status = %q, want up", got)
	}
}
