package filewatcher

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/ai-shift/tracer/config"
	"github.com/ai-shift/tracer/core/services/logparser"
)

const (
	pollInterval = 100 * time.Millisecond
	maxBatchSize = 100
)

type watchedFile struct {
	path   string
	file   *os.File
	parser logparser.Parser
	offset int64
}

type Service struct {
	LogChan chan logparser.LogEntry
	files   []*watchedFile
	mu      sync.Mutex
}

func New(cfgFiles []config.LogFile) (*Service, error) {
	s := &Service{
		LogChan: make(chan logparser.LogEntry, 1000),
		files:   make([]*watchedFile, 0, len(cfgFiles)),
	}

	for _, cfg := range cfgFiles {
		parser, err := logparser.New(cfg)
		if err != nil {
			return nil, err
		}

		wf := &watchedFile{
			path:   cfg.Path,
			parser: parser,
		}
		s.files = append(s.files, wf)
	}

	return s, nil
}

func (s *Service) Start(ctx context.Context) {
	// Open all files
	for _, wf := range s.files {
		if err := s.openFile(wf); err != nil {
			slog.Error("Failed to open file", "path", wf.path, "error", err)
		}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.closeAll()
			return
		case <-ticker.C:
			s.pollAll(ctx)
		}
	}
}

func (s *Service) openFile(wf *watchedFile) error {
	file, err := os.Open(wf.path)
	if err != nil {
		return err
	}

	// Seek to end to only read new lines
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		file.Close()
		return err
	}

	wf.file = file
	wf.offset = offset
	slog.Info("Watching file", "path", wf.path, "offset", offset)
	return nil
}

func (s *Service) pollAll(ctx context.Context) {
	for _, wf := range s.files {
		if wf.file == nil {
			// Try to reopen
			if err := s.openFile(wf); err != nil {
				continue
			}
		}
		s.readNewLines(ctx, wf)
	}
}

func (s *Service) readNewLines(ctx context.Context, wf *watchedFile) {
	// Check if file was truncated or rotated
	info, err := wf.file.Stat()
	if err != nil {
		slog.Warn("Failed to stat file", "path", wf.path, "error", err)
		s.reopenFile(wf)
		return
	}

	if info.Size() < wf.offset {
		// File was truncated, reopen
		slog.Info("File truncated, reopening", "path", wf.path)
		s.reopenFile(wf)
		return
	}

	// Read new lines
	scanner := bufio.NewScanner(wf.file)
	count := 0

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		entry, err := wf.parser.Parse(line)
		if err != nil {
			slog.Debug("Failed to parse line", "error", err, "line", line)
			continue
		}

		select {
		case s.LogChan <- *entry:
		default:
			slog.Warn("Log channel full, dropping entry")
		}

		count++
		if count >= maxBatchSize {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("Scanner error", "path", wf.path, "error", err)
	}

	// Update offset
	newOffset, _ := wf.file.Seek(0, io.SeekCurrent)
	wf.offset = newOffset
}

func (s *Service) reopenFile(wf *watchedFile) {
	if wf.file != nil {
		wf.file.Close()
		wf.file = nil
	}

	file, err := os.Open(wf.path)
	if err != nil {
		slog.Warn("Failed to reopen file", "path", wf.path, "error", err)
		return
	}

	wf.file = file
	wf.offset = 0
}

func (s *Service) closeAll() {
	for _, wf := range s.files {
		if wf.file != nil {
			wf.file.Close()
			wf.file = nil
		}
	}
	close(s.LogChan)
}
