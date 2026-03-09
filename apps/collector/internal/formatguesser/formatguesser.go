package formatguesser

import (
	"context"
	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/source"
)

func Add(ctx context.Context, cfg config.Config, ss source.SourceSeeker) {
}

func guessFormat(raw []byte) (config.Config, error)
