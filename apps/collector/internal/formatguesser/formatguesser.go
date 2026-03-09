package formatguesser

import (
	"context"
	"io"
	"fmt"
	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/source"
)

func Add(ctx context.Context, cfg *config.Config, ss source.SourceSeeker) error {
	if err := ss.Open(ctx); err != nil {
		return fmt.Errorf("failed to open the source, error: %v", err)
	}
	
	logSize := int64(100)
	var logs string
	var err error
	found := false
	for !found && logSize > 1 {
		if _, err = ss.Seek(-logSize, io.SeekEnd); err != nil {
			return fmt.Errorf("failed to seek in the source, error: %v", err)		
		}
		if logs, err = ss.Next(ctx); err != nil {
			logSize = logSize / 2
			continue
		}
		found = true
	}

	if err = guessFormat(logs, cfg); err != nil {
		return fmt.Errorf("failed to guess the logs format, error: %v", err)
	}

	return nil
}

func guessFormat(lines string, cfg *config.Config) (error)
