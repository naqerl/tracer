package source

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
)

// fileSource reads from a log file.
type fileSource struct {
	app  string
	path string
	from string // "start" or "end"
}

const (
	readFromStart = "start"
	readFromEnd   = "end"

	pollInterval = 250 * time.Millisecond
)

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

	file, err := os.Open(fs.path)
	if err != nil {
		slog.Error("failed to open log file", "app", fs.app, "path", fs.path, "error", err)
		return
	}
	defer file.Close()

	if strings.EqualFold(fs.from, readFromEnd) {
		if _, err := file.Seek(0, io.SeekEnd); err != nil {
			slog.Error("failed to seek to file end", "app", fs.app, "path", fs.path, "error", err)
			return
		}
	}

	reader := bufio.NewReader(file)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					if err == io.EOF {
						break
					}
					slog.Error("file read failed", "app", fs.app, "path", fs.path, "error", err)
					break
				}
				line = strings.TrimRight(line, "\r\n")
				if line == "" {
					continue
				}
				select {
				case out <- line:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}
