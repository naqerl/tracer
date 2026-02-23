package supervisor

import (
	"context"
	"log/slog"
	"time"

	"github.com/ai-shift/tracer/apps/collector/internal/events"
	"github.com/ai-shift/tracer/libs/types"
)

// Source is the interface for supervised sources.
type Source interface {
	Read(ctx context.Context) <-chan types.RawLine
	Health(ctx context.Context) <-chan error
	Name() string
	AppName() string
	Restart() error
}

// Supervisor monitors sources and restarts unhealthy ones.
type Supervisor struct {
	sources        []Source
	bus            *events.Bus
	restartBackoff time.Duration
}

// New creates a new supervisor.
func New(sources []Source, bus *events.Bus) *Supervisor {
	return &Supervisor{
		sources:        sources,
		bus:            bus,
		restartBackoff: 5 * time.Second,
	}
}

// WithRestartBackoff configures the backoff between restart attempts.
func (s *Supervisor) WithRestartBackoff(d time.Duration) *Supervisor {
	s.restartBackoff = d
	return s
}

// Run starts the supervisor and blocks until context is cancelled.
func (s *Supervisor) Run(ctx context.Context) {
	// Start monitoring each source
	for _, src := range s.sources {
		go s.monitorSource(ctx, src)
	}

	<-ctx.Done()
}

func (s *Supervisor) monitorSource(ctx context.Context, src Source) {
	for {
		// Check if context is done
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Get health channel for this source
		healthCh := src.Health(ctx)

		// Wait for health error
		var err error
		select {
		case err = <-healthCh:
			if err == nil {
				// Channel closed without error (context cancelled)
				return
			}
		case <-ctx.Done():
			return
		}

		slog.Warn("Source unhealthy", "source", src.Name(), "error", err)
		s.emitError(src, "unhealthy", err)

		// Attempt restart with backoff
		if restartErr := s.restartSource(ctx, src); restartErr != nil {
			slog.Error("Failed to restart source", "source", src.Name(), "error", restartErr)
			s.emitError(src, "restart_failed", restartErr)
		}
	}
}

func (s *Supervisor) restartSource(ctx context.Context, src Source) error {
	backoff := s.restartBackoff
	maxBackoff := 5 * time.Minute

	for {
		slog.Info("Restarting source", "source", src.Name())
		if err := src.Restart(); err != nil {
			s.emitError(src, "restart_error", err)
			backoff = min(backoff*2, maxBackoff)

			select {
			case <-time.After(backoff):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
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
