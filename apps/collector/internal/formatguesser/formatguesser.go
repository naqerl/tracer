package formatguesser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"tracer/apps/collector/internal/config"
	"tracer/apps/collector/internal/parser"
	"tracer/apps/collector/internal/source"
)

func Add(ctx context.Context, cfg *config.Config, ss source.SourceSeeker) error {
	if err := ss.Open(ctx); err != nil {
		return fmt.Errorf("failed to open the source, error: %v", err)
	}
	
	logSize := int64(100)
	var logErr error
	found := false
	for !found && logSize > 1 {
		if _, logErr = ss.Seek(-logSize, io.SeekEnd); logErr != nil {
			return fmt.Errorf("failed to seek in the source, error: %v", logErr)
		}
		if _, logErr = ss.Next(ctx); logErr != nil {
			logSize = logSize / 2
			continue
		}
		found = true
	}
	if !found {
		return errors.New("failed to find logs in the source")
	}

	var prev parser.Spec
	var curr parser.Spec
	var formatErr error
	var line string
	for {
		line, formatErr  = ss.Next(ctx)
		if formatErr != nil {
			if errors.Is(formatErr, io.EOF) {
				break
			}
		}
		curr, formatErr = guessFormat(line)
		if formatErr != nil {
			return fmt.Errorf("failed to guess the logs format, error: %v", formatErr)
		}
		prev, formatErr = compareOrUpdate(prev, curr)
		if formatErr != nil {
			return fmt.Errorf("failed to check the log consistency, error: %v", formatErr)
		}

	}

	return nil
}

func guessFormat(line string) (parser.Spec ,error)

func compareOrUpdate(prev, curr parser.Spec) (parser.Spec, error ) {
	if prev.Type == "" {
		prev.Type = curr.Type
	} else if prev.Type != curr.Type {
		return prev, fmt.Errorf("spec types mismatch. Prev: %s, Curr: %s", prev.Type, curr.Type)
	}

	if prev.Params == nil {
		prev.Params = make(map[string]string)
	}

	for k,currV := range curr.Params {
		prevV, ok := prev.Params[k]
		if !ok {
			prev.Params[k] = currV
		} else if prevV != currV {
			return prev, fmt.Errorf("spec params mismatch, param: %s. Prev: %s, Curr: %s", k, prevV, currV)
		}
	}

	return prev, nil
}
