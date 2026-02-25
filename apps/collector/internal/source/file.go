package source

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// FileConfig contains configuration for file source.
type FileConfig struct {
	Path     string
	ReadFrom string
}

// fileSource reads from a log file.
type fileSource struct {
	path   string
	from   string // "start" or "end"
	file   *os.File
	reader *bufio.Reader
}

const (
	readFromEnd = "end"

	pollInterval = 250 * time.Millisecond
)

// NewFile creates a new file source.
// Enforces creation of the error channel for health monitoring.
func NewFile(cfg FileConfig) Source {
	return &fileSource{
		path: cfg.Path,
		from: cfg.ReadFrom,
	}
}

func (fs *fileSource) Open(context.Context) error {
	file, err := os.Open(fs.path)
	if err != nil {
		return fmt.Errorf("file source open (%s): %w", fs.path, err)
	}

	if strings.EqualFold(fs.from, readFromEnd) {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			_ = file.Close()
			return fmt.Errorf("file source seek end (%s): %w", fs.path, err)
		}
	}

	fs.file = file
	fs.reader = bufio.NewReader(file)
	return nil
}

func (fs *fileSource) Next(ctx context.Context) (string, error) {
	if fs.reader == nil {
		return "", fmt.Errorf("file source not open")
	}

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		line, err := fs.reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				timer := time.NewTimer(pollInterval)
				select {
				case <-ctx.Done():
					timer.Stop()
					return "", ctx.Err()
				case <-timer.C:
					continue
				}
			}
			return "", fmt.Errorf("file source read (%s): %w", fs.path, err)
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		return line, nil
	}
}

func (fs *fileSource) Close() error {
	if fs.file == nil {
		return nil
	}
	err := fs.file.Close()
	fs.file = nil
	fs.reader = nil
	return err
}
