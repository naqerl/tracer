// Package dropstats aggregates unparseable log lines for the collector's
// stats interval.
package dropstats

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	"tracer/libs/types"
)

// Tracker counts unparseable lines per app between stats reports.
type Tracker struct {
	logger *slog.Logger

	mu       sync.Mutex
	apps     map[string]*appDrops
	selfApps map[string]struct{}
}

// New creates a tracker that reports summaries through logger.
func New(logger *slog.Logger) *Tracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Tracker{
		logger:   logger,
		apps:     make(map[string]*appDrops),
		selfApps: make(map[string]struct{}),
	}
}

// Record adds one unparseable line to the app's current interval.
func (t *Tracker) Record(app, line string) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	state, ok := t.apps[app]
	if !ok {
		state = &appDrops{}
		t.apps[app] = state
	}
	state.window++
	state.total++
	state.lastSample = truncateLine(line, 200)
	state.updatedAt = time.Now().UTC()
}

// SetSelfApps replaces the app names whose summaries are suppressed.
func (t *Tracker) SetSelfApps(apps []string) {
	if t == nil {
		return
	}

	self := make(map[string]struct{}, len(apps))
	for _, app := range apps {
		self[app] = struct{}{}
	}

	t.mu.Lock()
	t.selfApps = self
	t.mu.Unlock()
}

// Report resets every app's interval and returns the dropped-line counts for
// apps that dropped at least one line. It logs one warning per non-self app.
func (t *Tracker) Report() map[string]types.AppDropStats {
	if t == nil {
		return nil
	}

	type warning struct {
		app    string
		count  int64
		total  int64
		sample string
	}

	var warnings []warning
	snapshot := make(map[string]types.AppDropStats)

	t.mu.Lock()
	for app, state := range t.apps {
		if state.window == 0 {
			continue
		}
		snapshot[app] = types.AppDropStats{
			DroppedLines: state.window,
			LastSample:   state.lastSample,
			UpdatedAt:    state.updatedAt,
		}
		if _, self := t.selfApps[app]; !self {
			warnings = append(warnings, warning{
				app:    app,
				count:  state.window,
				total:  state.total,
				sample: state.lastSample,
			})
		}
		state.window = 0
	}
	t.mu.Unlock()

	sort.Slice(warnings, func(i, j int) bool {
		return warnings[i].app < warnings[j].app
	})
	for _, w := range warnings {
		t.logger.Warn("dropped unparseable log lines",
			"app", w.app,
			"count", w.count,
			"total", w.total,
			"sample", w.sample)
	}

	if len(snapshot) == 0 {
		return nil
	}
	return snapshot
}

type appDrops struct {
	window     int64
	total      int64
	lastSample string
	updatedAt  time.Time
}

func truncateLine(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
