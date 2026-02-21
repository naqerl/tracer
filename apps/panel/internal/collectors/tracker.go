package collectors

import (
	"sort"
	"sync"
	"time"

	"github.com/ai-shift/tracer/libs/types"
)

// Collector represents a connected collector instance
type Collector struct {
	Name        string     `json:"name"`
	RemoteAddr  string     `json:"remote_addr"`
	Apps        []string   `json:"apps"`
	Thresholds types.CollectorThresholds `json:"thresholds"`
	ConnectedAt time.Time  `json:"connected_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	IsConnected bool       `json:"is_connected"`
	connID      string     // internal: current connection ID
}

// Tracker keeps track of connected collectors
type Tracker struct {
	mu         sync.RWMutex
	collectors map[string]*Collector // key: collector name
}

// NewTracker creates a new collector tracker
func NewTracker() *Tracker {
	return &Tracker{
		collectors: make(map[string]*Collector),
	}
}

// Register adds or updates a collector connection
// Uses collector name as key, so reconnections update the existing entry
func (t *Tracker) Register(connID, name, remoteAddr string, apps []string, thresholds types.CollectorThresholds) {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	if c, exists := t.collectors[name]; exists {
		// Update existing collector (reconnection)
		c.connID = connID
		c.RemoteAddr = remoteAddr
		c.Apps = apps
		c.Thresholds = thresholds
		c.LastSeenAt = now
		c.IsConnected = true
		// Keep original ConnectedAt for session tracking
	} else {
		// New collector
		t.collectors[name] = &Collector{
			connID:      connID,
			Name:        name,
			RemoteAddr:  remoteAddr,
			Apps:        apps,
			Thresholds:  thresholds,
			ConnectedAt: now,
			LastSeenAt:  now,
			IsConnected: true,
		}
	}
}

// Disconnect marks a collector as disconnected (by connection ID)
// Only disconnects if the connID matches the current connection
func (t *Tracker) Disconnect(connID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, c := range t.collectors {
		if c.connID == connID {
			c.IsConnected = false
			c.LastSeenAt = time.Now()
			return
		}
	}
}

// UpdateLastSeen updates the last seen timestamp for a collector (by connection ID)
func (t *Tracker) UpdateLastSeen(connID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, c := range t.collectors {
		if c.connID == connID {
			c.LastSeenAt = time.Now()
			return
		}
	}
}

// List returns all collectors (both connected and recently disconnected)
// Sorted by name for consistent ordering
func (t *Tracker) List() []Collector {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]Collector, 0, len(t.collectors))
	for _, c := range t.collectors {
		result = append(result, *c)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
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
	for name, c := range t.collectors {
		if !c.IsConnected && c.LastSeenAt.Before(cutoff) {
			delete(t.collectors, name)
		}
	}
}

// Get returns a specific collector by name
func (t *Tracker) Get(name string) (Collector, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if c, exists := t.collectors[name]; exists {
		return *c, true
	}
	return Collector{}, false
}
