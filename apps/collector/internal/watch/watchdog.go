package watchdog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
	logparser "github.com/ai-shift/tracer/apps/collector/internal/parsing"
	"github.com/ai-shift/tracer/libs/types"
)

const (
	pollInterval = 100 * time.Millisecond
	maxBatchSize = 100
	maxLineBytes = 1024 * 1024

	readFromEnd = "end"
)

type sourceType int

const (
	sourceTypeFile sourceType = iota
	sourceTypeJournalctl
)

type sourceConfig interface {
	sourceType() sourceType
	label() string
}

type fileSourceConfig struct {
	path     string
	readFrom string
}

func (c fileSourceConfig) sourceType() sourceType { return sourceTypeFile }
func (c fileSourceConfig) label() string          { return c.path }

type journalctlSourceConfig struct {
	unit  string
	since string
}

func (c journalctlSourceConfig) sourceType() sourceType { return sourceTypeJournalctl }
func (c journalctlSourceConfig) label() string          { return "journalctl:" + c.unit }

type AppLogEntry struct {
	AppName   string
	Entry     types.LogEntry
	IsStartup bool
}

// watchedSource is thread-safe for concurrent access.
type watchedSource struct {
	mu      sync.RWMutex
	appName string
	source  sourceConfig
	parser  logparser.Parser

	// File-specific fields
	file   *os.File
	offset atomic.Int64 // Use atomic for lock-free offset reads

	// Journalctl-specific fields
	cmd    *exec.Cmd
	stdout io.ReadCloser

	startupRegex *regexp.Regexp

	// Buffer pool for scanning - initialized per source
	scanBuf []byte
}

type Service struct {
	LogChan   chan AppLogEntry
	sources   []*watchedSource
	closeOnce sync.Once // Protects channel close
	wg        sync.WaitGroup
}

func New(apps []collectorconfig.ResolvedApp) (*Service, error) {
	s := &Service{
		LogChan: make(chan AppLogEntry, 1000),
		sources: make([]*watchedSource, 0, len(apps)),
	}

	for _, app := range apps {
		parser, err := logparser.New(app)
		if err != nil {
			return nil, fmt.Errorf("creating parser for app %s: %w", app.Name, err)
		}

		sourceCfg, err := buildSourceConfig(app)
		if err != nil {
			return nil, err
		}

		ws := &watchedSource{
			appName: app.Name,
			parser:  parser,
			source:  sourceCfg,
			scanBuf: make([]byte, 0, 64*1024),
		}

		if app.StartupPattern != "" {
			re, err := regexp.Compile(app.StartupPattern)
			if err != nil {
				slog.Warn("Invalid startup pattern, ignoring", "app", app.Name, "pattern", app.StartupPattern, "error", err)
			} else {
				ws.startupRegex = re
				slog.Info("Startup detection enabled", "app", app.Name, "pattern", app.StartupPattern)
			}
		}

		s.sources = append(s.sources, ws)
	}

	return s, nil
}

func buildSourceConfig(app collectorconfig.ResolvedApp) (sourceConfig, error) {
	switch app.SourceKind {
	case "journalctl":
		return journalctlSourceConfig{unit: app.Unit, since: app.Since}, nil
	case "file":
		return fileSourceConfig{path: app.Path, readFrom: app.ReadFrom}, nil
	default:
		return nil, fmt.Errorf("unknown source kind: %s", app.SourceKind)
	}
}

func (s *Service) GetAppNames() []string {
	names := make([]string, 0, len(s.sources))
	for _, ws := range s.sources {
		names = append(names, ws.appName)
	}
	return names
}

func (s *Service) Start(ctx context.Context) {
	// Initialize all sources
	for _, ws := range s.sources {
		switch cfg := ws.source.(type) {
		case fileSourceConfig:
			if err := s.openFile(ws, cfg); err != nil {
				slog.Error("Failed to open file", "path", cfg.path, "error", err)
			}
		case journalctlSourceConfig:
			if err := s.startJournalctl(ctx, ws, cfg); err != nil {
				slog.Error("Failed to start journalctl", "unit", cfg.unit, "error", err)
			}
		}
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.closeAll()
			s.wg.Wait() // Wait for all goroutines to finish
			return
		case <-ticker.C:
			s.pollAll(ctx)
		}
	}
}

func (s *Service) openFile(ws *watchedSource, cfg fileSourceConfig) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	file, err := os.Open(cfg.path)
	if err != nil {
		return fmt.Errorf("opening file %s: %w", cfg.path, err)
	}

	offset := int64(0)
	if cfg.readFrom == readFromEnd {
		offset, err = file.Seek(0, io.SeekEnd)
		if err != nil {
			file.Close()
			return fmt.Errorf("seeking to end of %s: %w", cfg.path, err)
		}
	}

	ws.file = file
	ws.offset.Store(offset)
	slog.Info("Watching file", "path", cfg.path, "offset", offset)
	return nil
}

func (s *Service) startJournalctl(ctx context.Context, ws *watchedSource, cfg journalctlSourceConfig) error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	cmd := exec.CommandContext(ctx, "journalctl",
		"-u", cfg.unit,
		"-f",
		"-o", "json",
		"--since", cfg.since,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe for journalctl: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting journalctl: %w", err)
	}

	ws.cmd = cmd
	ws.stdout = stdout

	slog.Info("Watching journalctl", "unit", cfg.unit)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.readJournalctl(ctx, ws, cfg.unit)
	}()

	return nil
}

func (s *Service) readJournalctl(ctx context.Context, ws *watchedSource, unit string) {
	ws.mu.RLock()
	stdout := ws.stdout
	scanBuf := ws.scanBuf
	ws.mu.RUnlock()

	if stdout == nil {
		return
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(scanBuf, maxLineBytes)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		if stop := s.parseAndDispatch(ctx, ws, line, "journalctl:"+unit); stop {
			return
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("Journalctl scanner error", "unit", unit, "error", err)
	}
}

func extractJournalMessage(line string) (string, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return "", fmt.Errorf("unmarshaling journal json: %w", err)
	}
	msg, ok := data["MESSAGE"]
	if !ok {
		return "", errors.New("missing MESSAGE field in journal entry")
	}
	return fmt.Sprintf("%v", msg), nil
}

func (ws *watchedSource) isStartupEntry(entry types.LogEntry) bool {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	if ws.startupRegex == nil {
		return false
	}
	return ws.startupRegex.MatchString(entry.Message)
}

func (s *Service) pollAll(ctx context.Context) {
	for _, ws := range s.sources {
		cfg, ok := ws.source.(fileSourceConfig)
		if !ok {
			continue
		}

		ws.mu.Lock()
		needsOpen := ws.file == nil
		ws.mu.Unlock()

		if needsOpen {
			if err := s.openFile(ws, cfg); err != nil {
				continue
			}
		}
		s.readNewLines(ctx, ws, cfg.path)
	}
}

func (s *Service) readNewLines(ctx context.Context, ws *watchedSource, path string) {
	ws.mu.Lock()
	file := ws.file
	scanBuf := ws.scanBuf
	ws.mu.Unlock()

	if file == nil {
		return
	}

	info, err := file.Stat()
	if err != nil {
		slog.Warn("Failed to stat file", "path", path, "error", err)
		s.reopenFile(ws, path)
		return
	}

	ws.mu.RLock()
	currentOffset := ws.offset.Load()
	ws.mu.RUnlock()

	if info.Size() < currentOffset {
		slog.Info("File truncated, reopening", "path", path)
		s.reopenFile(ws, path)
		return
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(scanBuf, maxLineBytes)

	count := 0
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		if stop := s.parseAndDispatch(ctx, ws, line, path); stop {
			return
		}

		count++
		if count >= maxBatchSize {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("Scanner error", "path", path, "error", err)
	}

	newOffset, _ := file.Seek(0, io.SeekCurrent)
	ws.offset.Store(newOffset)
}

// parseAndDispatch is the single parse path for all sources.
// Returns stop=true means caller should terminate loop.
func (s *Service) parseAndDispatch(ctx context.Context, ws *watchedSource, line, source string) (stop bool) {
	entry, err := s.parseLine(ws, line)
	if err != nil {
		slog.Debug("Failed to parse line", "app", ws.appName, "source", source, "error", err)
		return false
	}

	isStartup := ws.isStartupEntry(entry)
	if isStartup {
		slog.Info("Detected app startup", "app", ws.appName, "message", entry.Message)
	}

	select {
	case s.LogChan <- AppLogEntry{AppName: ws.appName, Entry: entry, IsStartup: isStartup}:
		return false
	case <-ctx.Done():
		return true
	}
}

func (s *Service) parseLine(ws *watchedSource, line string) (types.LogEntry, error) {
	ws.mu.RLock()
	parser := ws.parser
	sourceType := ws.source.sourceType()
	ws.mu.RUnlock()

	entry, err := parser.Parse(line)
	if err == nil {
		return entry, nil
	}

	// For journalctl source, try parsing extracted MESSAGE payload
	if sourceType == sourceTypeJournalctl {
		raw, extractErr := extractJournalMessage(line)
		if extractErr == nil {
			if retryEntry, retryErr := parser.Parse(raw); retryErr == nil {
				return retryEntry, nil
			}
		}
	}

	return types.LogEntry{}, fmt.Errorf("parsing log line: %w", err)
}

func (s *Service) reopenFile(ws *watchedSource, path string) {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	if ws.file != nil {
		if err := ws.file.Close(); err != nil {
			slog.Warn("Error closing file during reopen", "path", path, "error", err)
		}
		ws.file = nil
	}

	file, err := os.Open(path)
	if err != nil {
		slog.Warn("Failed to reopen file", "path", path, "error", err)
		return
	}

	ws.file = file
	ws.offset.Store(0)
}

func (s *Service) closeAll() {
	s.closeOnce.Do(func() {
		for _, ws := range s.sources {
			switch ws.source.sourceType() {
			case sourceTypeFile:
				ws.mu.Lock()
				if ws.file != nil {
					if err := ws.file.Close(); err != nil {
						slog.Warn("Error closing file", "path", ws.source.label(), "error", err)
					}
					ws.file = nil
				}
				ws.mu.Unlock()

			case sourceTypeJournalctl:
				ws.mu.Lock()
				// Try graceful shutdown first
				if ws.cmd != nil && ws.cmd.Process != nil {
					if err := ws.cmd.Process.Signal(os.Interrupt); err != nil {
						slog.Debug("Failed to send interrupt, killing", "unit", ws.source.label(), "error", err)
						_ = ws.cmd.Process.Kill()
					}
				}
				if ws.stdout != nil {
					if err := ws.stdout.Close(); err != nil {
						slog.Warn("Error closing journalctl stdout", "unit", ws.source.label(), "error", err)
					}
				}
				ws.mu.Unlock()
			}
		}
		close(s.LogChan)
	})
}
