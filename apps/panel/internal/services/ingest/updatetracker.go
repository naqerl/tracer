package ingest

import (
	"encoding/json"
	"sync"
	"sync/atomic"

	"tracer/libs/types"
)

// UpdateStage holds the current status of a single update stage.
type UpdateStage struct {
	Stage   string               `json:"stage"`
	Status  types.ProgressStatus `json:"status"`
	Message string               `json:"message,omitempty"`
}

// UpdateEntityProgress holds the progress for a single entity being updated.
type UpdateEntityProgress struct {
	Entity string        `json:"entity"`
	Stages []UpdateStage `json:"stages"`
	Done   bool          `json:"done"`
}

// UpdateTracker tracks progress of update operations and provides
// an SSE subscriber channel for real-time progress streaming.
type UpdateTracker struct {
	mu       sync.RWMutex
	entities map[string]*UpdateEntityProgress

	// subscribers receive progress events encoded as JSON bytes.
	subs   map[chan []byte]struct{}
	subsMu sync.Mutex

	// active indicates an update is currently in progress (atomic bool).
	active int32
}

// NewUpdateTracker creates a new tracker.
func NewUpdateTracker() *UpdateTracker {
	return &UpdateTracker{
		entities: make(map[string]*UpdateEntityProgress),
		subs:     make(map[chan []byte]struct{}),
	}
}

// Subscribe returns a channel that receives progress events as JSON bytes.
// The caller must call Unsubscribe when done.
func (t *UpdateTracker) Subscribe() chan []byte {
	ch := make(chan []byte, 64)
	t.subsMu.Lock()
	t.subs[ch] = struct{}{}
	t.subsMu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel.
func (t *UpdateTracker) Unsubscribe(ch chan []byte) {
	t.subsMu.Lock()
	delete(t.subs, ch)
	t.subsMu.Unlock()
}

// IsActive returns true if an update is currently in progress.
func (t *UpdateTracker) IsActive() bool {
	return atomic.LoadInt32(&t.active) == 1
}

// StartUpdate marks the update as active and initializes entities.
func (t *UpdateTracker) StartUpdate() {
	atomic.StoreInt32(&t.active, 1)
	t.mu.Lock()
	t.entities = make(map[string]*UpdateEntityProgress)
	t.mu.Unlock()
}

// FinishUpdate marks the update as complete (no longer active).
func (t *UpdateTracker) FinishUpdate() {
	atomic.StoreInt32(&t.active, 0)
}

// stageOrder defines the expected order of stages for display.
var stageOrder = []string{"download", "build", "verify", "install", "restart", "complete"}

func stageIndex(stage string) int {
	for i, s := range stageOrder {
		if s == stage {
			return i
		}
	}
	return len(stageOrder)
}

// ReportProgress records progress for an entity and broadcasts to all subscribers.
func (t *UpdateTracker) ReportProgress(entity, stage string, status types.ProgressStatus, message string) {
	t.mu.Lock()
	ep, ok := t.entities[entity]
	if !ok {
		ep = &UpdateEntityProgress{Entity: entity}
		t.entities[entity] = ep
	}

	// Find and update existing stage, or append new one
	found := false
	for i, s := range ep.Stages {
		if s.Stage == stage {
			ep.Stages[i].Status = status
			if message != "" {
				ep.Stages[i].Message = message
			}
			found = true
			break
		}
	}
	if !found {
		ep.Stages = append(ep.Stages, UpdateStage{
			Stage:   stage,
			Status:  status,
			Message: message,
		})
	}

	// Sort stages by order
	// (simple insertion-sort-like approach since stages are usually appended in order)
	stabilized := make([]UpdateStage, len(ep.Stages))
	copy(stabilized, ep.Stages)
	for i := 0; i < len(stabilized); i++ {
		for j := i + 1; j < len(stabilized); j++ {
			if stageIndex(stabilized[j].Stage) < stageIndex(stabilized[i].Stage) {
				stabilized[i], stabilized[j] = stabilized[j], stabilized[i]
			}
		}
	}
	ep.Stages = stabilized

	ep.Done = (status == types.ProgressFailed || (stage == "complete" && status == types.ProgressDone))
	t.mu.Unlock()

	// Broadcast to all subscribers
	evt := UpdateEventJSON{
		Type:     "progress",
		Entity:   entity,
		Stage:    stage,
		Status:   string(status),
		Message:  message,
		Entities: t.Snapshot(),
	}

	t.subsMu.Lock()
	for ch := range t.subs {
		select {
		case ch <- evt.Marshal():
		default:
			// Drop if subscriber is too slow
		}
	}
	t.subsMu.Unlock()
}

// Snapshot returns a copy of all entity progress states.
func (t *UpdateTracker) Snapshot() []UpdateEntityProgress {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]UpdateEntityProgress, 0, len(t.entities))
	for _, ep := range t.entities {
		result = append(result, *ep)
	}
	return result
}

// UpdateEventJSON is the JSON structure sent to SSE subscribers.
type UpdateEventJSON struct {
	Type     string                 `json:"type"`
	Entity   string                 `json:"entity,omitempty"`
	Stage    string                 `json:"stage,omitempty"`
	Status   string                 `json:"status,omitempty"`
	Message  string                 `json:"message,omitempty"`
	Entities []UpdateEntityProgress `json:"entities,omitempty"`
}

// Marshal serializes the event to JSON bytes.
func (e UpdateEventJSON) Marshal() []byte {
	b, _ := json.Marshal(e)
	return b
}
