package supervisor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ai-shift/tracer/apps/collector/internal/events"
	"github.com/ai-shift/tracer/libs/types"
)

// Source is the interface for supervised sources.
type Source interface {
	Read(ctx context.Context) <-chan types.RawLine
	Name() string
	AppName() string
	Health() error
	Restart() error
}

// Supervisor monitors sources and restarts unhealthy ones.
type Supervisor struct {
	sources        []Source
	bus            *events.Bus
	checkInterval  time.Duration
	restartBackoff time.Duration
}

// New creates a new supervisor.
func New(sources []Source, bus *events.Bus) *Supervisor {
	return &Supervisor{
		sources:        sources,
		bus:            bus,
		checkInterval:  30 * time.Second,
		restartBackoff: 5 * time.Second,
	}
}

// WithCheckInterval configures the health check interval.
func (s *Supervisor) WithCheckInterval(d time.Duration) *Supervisor {
	s.checkInterval = d
	return s
}

// WithRestartBackoff configures the backoff between restart attempts.
func (s *Supervisor) WithRestartBackoff(d time.Duration) *Supervisor {
	s.restartBackoff = d
	return s
}

// Run starts the supervisor and blocks until context is cancelled.
func (s *Supervisor) Run(ctx context.Context) {
	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	// Initial health check
	s.checkAll(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkAll(ctx)
		}
	}
}

func (s *Supervisor) checkAll(ctx context.Context) {
	for _, src := range s.sources {
		if err := src.Health(); err != nil {
			slog.Warn("Source unhealthy", "source", src.Name(), "error", err)
			s.emitError(src, "unhealthy", err)

			if err := s.restartSource(ctx, src); err != nil {
				slog.Error("Failed to restart source", "source", src.Name(), "error", err)
				s.emitError(src, "restart_failed", err)
			}
		}
	}
}

func (s *Supervisor) restartSource(ctx context.Context, src Source) error {
	backoff := s.restartBackoff
	maxBackoff := 5 * time.Minute

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}

		slog.Info("Restarting source", "source", src.Name())
		if err := src.Restart(); err != nil {
			s.emitError(src, "restart_error", err)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		// Verify health after restart
		if err := src.Health(); err != nil {
			s.emitError(src, "restart_unhealthy", err)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		slog.Info("Source restarted successfully", "source", src.Name())
		return nil
	}
}

func (s *Supervisor) emitError(src Source, errType string, err error) {
	evt := types.ErrorEvent{
		Time:    time.Now(),
		App:     src.AppName(),
		ErrType: errType,
		Message: err.Error(),
		Source:  src.Name(),
	}
	s.bus.Publish(evt)
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// ManagedSource wraps a source with automatic restart on failure.
type ManagedSource struct {
	Source
	mu      sync.RWMutex
	healthy bool
	lastErr error
}

// NewManagedSource creates a managed source wrapper.
func NewManagedSource(src Source) *ManagedSource {
	return &ManagedSource{Source: src, healthy: true}
}

// Health checks the underlying source and caches result.
func (ms *ManagedSource) Health() error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	err := ms.Source.Health()
	ms.lastErr = err
	ms.healthy = (err == nil)
	return err
}

// IsHealthy returns the cached health status.
func (ms *ManagedSource) IsHealthy() bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.healthy
}

// LastError returns the last health check error.
func (ms *ManagedSource) LastError() error {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.lastErr
}
