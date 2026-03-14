package health

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Checker interface {
	Check(ctx context.Context) Result
}

type Poller struct {
	checker  Checker
	interval time.Duration
}

func New(spec Spec) (Poller, error) {
	interval, err := spec.interval()
	if err != nil {
		return Poller{}, fmt.Errorf("health: %w", err)
	}

	var checker Checker
	switch strings.ToLower(strings.TrimSpace(spec.Type)) {
	case "":
		return Poller{}, fmt.Errorf("health type is required")
	case "http":
		httpConfig, err := spec.httpConfig()
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
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !send(p.checker.Check(ctx)) {
					return
				}
			}
		}
	}()
	return out
}
