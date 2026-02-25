package source

import (
	"context"
)

// fileSource reads from a log file.
type fileSource struct {
	app  string
	path string
	from string // "start" or "end"
}

// NewFile creates a new file source.
// Enforces creation of the error channel for health monitoring.
func NewFile(cfg FileConfig) Source {
	return &fileSource{
		app:  cfg.AppName,
		path: cfg.Path,
		from: cfg.ReadFrom,
	}
}

func (fs *fileSource) Lines(ctx context.Context) <-chan string {
	out := make(chan string)
	go fs.readLoop(ctx, out)
	return out
}

func (fs *fileSource) readLoop(ctx context.Context, out chan<- string) {
	defer close(out)
	panic("not implemented")
}
