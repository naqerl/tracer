// Package supervisor runs long-lived components (log sources, health pollers)
// and reconciles the running set against a desired configuration. When the
// panel pushes a new config, Apply starts, stops, or restarts only the
// components whose config changed — everything else keeps running.
package supervisor

import (
	"context"
	"sync"
)

// Component is a single long-running unit managed by the supervisor.
type Component interface {
	// ID uniquely and stably identifies the component across reloads
	// (e.g. "app:nginx"). Two components share an ID only when they are the
	// same logical component.
	ID() string
	// Signature is a fingerprint of the component's configuration. When it
	// changes between applies the component is restarted even if its ID is
	// unchanged.
	Signature() string
	// Run executes the component's work and returns when ctx is done.
	Run(ctx context.Context)
}

type running struct {
	cancel context.CancelFunc
	sig    string
}

// Supervisor reconciles a desired set of components against what is running.
type Supervisor struct {
	mu      sync.Mutex
	runners map[string]*running
}

// New creates an empty Supervisor.
func New() *Supervisor {
	return &Supervisor{runners: make(map[string]*running)}
}

// Apply reconciles the running set to desired. Components absent from desired
// are stopped; components whose signature changed are restarted; new
// components are started. Unchanged components keep running. Components that
// fail to start are retried by their own internal logic, not here.
func (s *Supervisor) Apply(ctx context.Context, desired []Component) {
	s.mu.Lock()
	defer s.mu.Unlock()

	want := make(map[string]Component, len(desired))
	for _, c := range desired {
		want[c.ID()] = c
	}

	// Stop removed or changed components.
	for id, r := range s.runners {
		c, ok := want[id]
		if !ok || c.Signature() != r.sig {
			r.cancel()
			delete(s.runners, id)
		}
	}

	// Start new or changed components.
	for id, c := range want {
		if _, ok := s.runners[id]; ok {
			continue // unchanged, already running
		}
		compCtx, cancel := context.WithCancel(ctx)
		s.runners[id] = &running{cancel: cancel, sig: c.Signature()}
		go c.Run(compCtx)
	}
}

// Stop cancels every running component.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runners {
		r.cancel()
	}
	s.runners = make(map[string]*running)
}
