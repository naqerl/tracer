package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"tracer/libs/types"
)

func TestProduceChecksImmediately(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	poller, err := New(types.HealthCheckSpec{
		Type: "http",
		Params: map[string]string{
			"url":             srv.URL,
			"interval":        "1h",
			"expected_status": "200",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	select {
	case result := <-poller.Produce(t.Context()):
		if result.Status != types.HealthStatusUp {
			t.Fatalf("status = %q, want up", result.Status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no result without waiting for the first interval")
	}
}

func TestProduceChecksEveryInterval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	poller, err := New(types.HealthCheckSpec{
		Type: "http",
		Params: map[string]string{
			"url":             srv.URL,
			"interval":        "20ms",
			"expected_status": "200",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	results := poller.Produce(t.Context())
	for i := 0; i < 3; i++ {
		select {
		case <-results:
		case <-time.After(5 * time.Second):
			t.Fatalf("only got %d results", i)
		}
	}
}

func TestProduceClosesOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	poller, err := New(types.HealthCheckSpec{
		Type: "http",
		Params: map[string]string{
			"url":             srv.URL,
			"interval":        "1h",
			"expected_status": "200",
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	results := poller.Produce(ctx)
	<-results
	cancel()

	select {
	case _, open := <-results:
		if open {
			t.Fatal("channel must be closed after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel was not closed after cancel")
	}
}
