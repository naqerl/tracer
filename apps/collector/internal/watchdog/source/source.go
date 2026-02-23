// Package source provides log source implementations.
package source

import (
	"context"
	"time"

	"github.com/ai-shift/tracer/libs/types"
)

// Source represents a log source (file or journalctl).
type Source interface {
	Read(ctx context.Context) <-chan types.RawLine
	Health() <-chan error
	Name() string
	AppName() string
	Restart() error
}

const (
	pollInterval = 100 * time.Millisecond
	maxBatchSize = 100
	maxLineBytes = 1024 * 1024
	readFromEnd  = "end"
)
