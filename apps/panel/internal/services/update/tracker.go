// Package update provides the panel-side update orchestrator.
// It manages update plans, coordinates prepare/activate phases across
// collectors, and exposes status via an in-memory tracker.
package update

import (
	"sync"
	"sync/atomic"
	"time"

	"tracer/libs/types"
)

// Status constants for update targets and plans.
const (
	StatusPending    = "pending"
	StatusPreparing  = "preparing"
	StatusPrepared   = "prepared"
	StatusActivating = "activating"
	StatusActivated  = "activated"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
	StatusCompleted  = "completed"

	StatusPendingRestart = "restarting"
)

// Plan is a high-level update plan stored in the database.
type Plan struct {
	ID          string     `json:"id"`
	Version     string     `json:"version"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Target is a single collector or self target within a plan.
type Target struct {
	ID           string     `json:"id"`
	PlanID       string     `json:"plan_id"`
	CollectorID  string     `json:"collector_id,omitempty"`
	TargetName   string     `json:"target_name"`
	Status       string     `json:"status"`
	Step         string     `json:"step"`
	Error        string     `json:"error,omitempty"`
	OldVersion   string     `json:"old_version,omitempty"`
	NewVersion   string     `json:"new_version,omitempty"`
	BinaryDigest string     `json:"binary_digest,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// Tracker holds in-memory progress for update plans. It is safe for
// concurrent access and supports subscription-style progress observation
// (currently via a simple callback; SSE is built on top).
type Tracker struct {
	mu          sync.RWMutex
	planEvents  map[string][]PlanEvent // planID → events
	subscribers map[int64]func(PlanEvent)
	nextSubID   atomic.Int64
}

// PlanEvent represents a state change in an update plan or target.
type PlanEvent struct {
	PlanID    string    `json:"plan_id"`
	Type      string    `json:"type"` // "plan_status", "target_status", "target_progress"
	Plan      *Plan     `json:"plan,omitempty"`
	Target    *Target   `json:"target,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// NewTracker creates a new Tracker.
func NewTracker() *Tracker {
	return &Tracker{
		planEvents:  make(map[string][]PlanEvent),
		subscribers: make(map[int64]func(PlanEvent)),
	}
}

// PublishPlanStatus records a plan-level status change and notifies subscribers.
func (t *Tracker) PublishPlanStatus(planID, status string) {
	t.mu.Lock()
	evt := PlanEvent{
		PlanID:    planID,
		Type:      "plan_status",
		Plan:      &Plan{ID: planID, Status: status},
		Timestamp: time.Now(),
	}
	t.planEvents[planID] = append(t.planEvents[planID], evt)
	subs := make([]func(PlanEvent), 0, len(t.subscribers))
	for _, fn := range t.subscribers {
		subs = append(subs, fn)
	}
	t.mu.Unlock()

	for _, sub := range subs {
		sub(evt)
	}
}

// PublishTargetStatus records a target-level status change.
func (t *Tracker) PublishTargetStatus(planID string, target *Target) {
	t.mu.Lock()
	evt := PlanEvent{
		PlanID:    planID,
		Type:      "target_status",
		Target:    target,
		Timestamp: time.Now(),
	}
	t.planEvents[planID] = append(t.planEvents[planID], evt)
	subs := make([]func(PlanEvent), 0, len(t.subscribers))
	for _, fn := range t.subscribers {
		subs = append(subs, fn)
	}
	t.mu.Unlock()

	for _, sub := range subs {
		sub(evt)
	}
}

// Events returns all events for a plan.
func (t *Tracker) Events(planID string) []PlanEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()
	events := t.planEvents[planID]
	result := make([]PlanEvent, len(events))
	copy(result, events)
	return result
}

// Subscribe adds a callback that is called for every event. Returns an
// unsubscribe function.
func (t *Tracker) Subscribe(fn func(PlanEvent)) func() {
	id := t.nextSubID.Add(1)
	t.mu.Lock()
	t.subscribers[id] = fn
	t.mu.Unlock()

	return func() {
		t.mu.Lock()
		delete(t.subscribers, id)
		t.mu.Unlock()
	}
}

// IngestUpdateStatus processes an UpdateStatusReport from a collector target.
func (t *Tracker) IngestUpdateStatus(planID, targetName string, report types.UpdateStatusReport) {
	target := &Target{
		TargetName: targetName,
		Status:     report.Status,
		Step:       report.Step,
		Error:      report.Error,
		OldVersion: report.OldVersion,
		NewVersion: report.NewVersion,
	}
	t.PublishTargetStatus(planID, target)
}
