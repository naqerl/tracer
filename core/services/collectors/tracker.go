package collectors

import (
	"sync"
	"time"
)

// Collector represents a connected collector instance
type Collector struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	RemoteAddr  string    `json:"remote_addr"`
	Apps        []string  `json:"apps"`
	ConnectedAt time.Time `json:"connected_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	IsConnected bool      `json:"is_connected"`
}

// Tracker keeps track of connected collectors
type Tracker struct {
	mu         sync.RWMutex
	collectors map[string]*Collector
}

// NewTracker creates a new collector tracker
func NewTracker() *Tracker {
	return &Tracker{
		collectors: make(map[string]*Collector),
	}
}

// Register adds or updates a collector connection
func (t *Tracker) Register(id, name, remoteAddr string, apps []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	if c, exists := t.collectors[id]; exists {
		// Update existing collector
		c.Name = name
		c.Apps = apps
		c.LastSeenAt = now
		c.IsConnected = true
	} else {
		// New collector
		t.collectors[id] = &Collector{
			ID:          id,
			Name:        name,
			RemoteAddr:  remoteAddr,
			Apps:        apps,
			ConnectedAt: now,
			LastSeenAt:  now,
			IsConnected: true,
		}
	}
}

// Disconnect marks a collector as disconnected
func (t *Tracker) Disconnect(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if c, exists := t.collectors[id]; exists {
		c.IsConnected = false
		c.LastSeenAt = time.Now()
	}
}

// UpdateLastSeen updates the last seen timestamp for a collector
func (t *Tracker) UpdateLastSeen(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if c, exists := t.collectors[id]; exists {
		c.LastSeenAt = time.Now()
	}
}

// List returns all collectors (both connected and recently disconnected)
func (t *Tracker) List() []Collector {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]Collector, 0, len(t.collectors))
	for _, c := range t.collectors {
		result = append(result, *c)
	}
	return result
}

// ListConnected returns only connected collectors
func (t *Tracker) ListConnected() []Collector {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]Collector, 0)
	for _, c := range t.collectors {
		if c.IsConnected {
			result = append(result, *c)
		}
	}
	return result
}

// Cleanup removes collectors that have been disconnected for longer than the given duration
func (t *Tracker) Cleanup(maxAge time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	for id, c := range t.collectors {
		if !c.IsConnected && c.LastSeenAt.Before(cutoff) {
			delete(t.collectors, id)
		}
	}
}

// Get returns a specific collector by ID
func (t *Tracker) Get(id string) (Collector, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if c, exists := t.collectors[id]; exists {
		return *c, true
	}
	return Collector{}, false
}
