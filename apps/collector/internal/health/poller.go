package health

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tracer/libs/types"
)

type Checker interface {
	Check(ctx context.Context) Result
}

type Poller struct {
	checker  Checker
	interval time.Duration
}

func New(spec types.HealthCheckSpec) (Poller, error) {
	interval, err := specInterval(spec)
	if err != nil {
		return Poller{}, fmt.Errorf("health: %w", err)
	}

	var checker Checker
	switch strings.ToLower(strings.TrimSpace(spec.Type)) {
	case "":
		return Poller{}, fmt.Errorf("health type is required")
	case "http":
		httpConfig, err := specHTTPConfig(spec)
		if err != nil {
			return Poller{}, fmt.Errorf("http health: %w", err)
		}
		httpChecker := NewHTTPChecker(httpConfig)
		checker = httpChecker
	case "systemd", "podman":
		return Poller{}, fmt.Errorf("health type %q is not implemented yet", spec.Type)
	default:
		return Poller{}, fmt.Errorf("unknown health type %q", spec.Type)
	}

	return Poller{
		checker:  checker,
		interval: interval,
	}, nil
}

// Produce checks the endpoint immediately and then once per interval, so a
// freshly created app reports its first result right away instead of after a
// full interval.
func (p Poller) Produce(ctx context.Context) <-chan Result {
	out := make(chan Result, 8)
	go func() {
		defer close(out)

		send := func(result Result) bool {
			select {
			case out <- result:
				return true
			case <-ctx.Done():
				return false
			}
		}

		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()

		for {
			if !send(p.checker.Check(ctx)) {
				return
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return out
}
