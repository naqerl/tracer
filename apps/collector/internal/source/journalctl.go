package source

import (
	"context"
)

// journalctlSource reads from systemd journal.
type journalctlSource struct {
	app   string
	unit  string
	since string
	errCh chan error
}

// NewJournalctl creates a new journalctl source.
// Enforces creation of the error channel for health monitoring.
func NewJournalctl(cfg JournalctlConfig) Source {
	return &journalctlSource{
		app:   cfg.AppName,
		unit:  cfg.Unit,
		since: cfg.Since,
		errCh: make(chan error, 1),
	}
}

func (js *journalctlSource) Lines(ctx context.Context) <-chan string {
	out := make(chan string)
	go js.readLoop(ctx, out)
	return out
}

func (js *journalctlSource) readLoop(ctx context.Context, out chan<- string) {
	defer close(out)
	panic("not implemented")
}
