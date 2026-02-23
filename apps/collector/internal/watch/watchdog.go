package watchdog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"time"

	collectorconfig "github.com/ai-shift/tracer/apps/collector/internal/config"
	"github.com/ai-shift/tracer/apps/collector/internal/parsing"
	"github.com/ai-shift/tracer/libs/types"
)

const (
	pollInterval = 100 * time.Millisecond
	maxBatchSize = 100
	maxLineBytes = 1024 * 1024
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

func (c fileSourceConfig) sourceType() sourceType {
	return sourceTypeFile
}

func (c fileSourceConfig) label() string {
	return c.path
}

type journalctlSourceConfig struct {
	unit  string
	since string
}

func (c journalctlSourceConfig) sourceType() sourceType {
	return sourceTypeJournalctl
}

func (c journalctlSourceConfig) label() string {
	return "journalctl:" + c.unit
}

type AppLogEntry struct {
	AppName   string
	Entry     types.LogEntry
	IsStartup bool
}

type watchedSource struct {
	appName string
	source  sourceConfig
	file    *os.File
	parser  logparser.Parser
	offset  int64

	startupRegex *regexp.Regexp

	cmd    *exec.Cmd
	stdout io.ReadCloser
}

type Service struct {
	LogChan chan AppLogEntry
	sources []*watchedSource
}

func New(apps []collectorconfig.ResolvedApp) (*Service, error) {
	s := &Service{
		LogChan: make(chan AppLogEntry, 1000),
		sources: make([]*watchedSource, 0, len(apps)),
	}

	for _, app := range apps {
		parser, err := logparser.New(app)
		if err != nil {
			return nil, err
		}

		sourceCfg, err := buildSourceConfig(app)
		if err != nil {
			return nil, err
		}

		ws := &watchedSource{
			appName: app.Name,
			parser:  parser,
			source:  sourceCfg,
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
			return
		case <-ticker.C:
			s.pollAll(ctx)
		}
	}
}

func (s *Service) openFile(ws *watchedSource, cfg fileSourceConfig) error {
	file, err := os.Open(cfg.path)
	if err != nil {
		return err
	}

	offset := int64(0)
	if cfg.readFrom == "end" {
		var err error
		offset, err = file.Seek(0, io.SeekEnd)
		if err != nil {
			file.Close()
			return err
		}
	}

	ws.file = file
	ws.offset = offset
	slog.Info("Watching file", "path", cfg.path, "offset", offset)
	return nil
}

func (s *Service) startJournalctl(ctx context.Context, ws *watchedSource, cfg journalctlSourceConfig) error {
	cmd := exec.CommandContext(ctx, "journalctl",
		"-u", cfg.unit,
		"-f",
		"-o", "json",
		"--since", cfg.since,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	ws.cmd = cmd
	ws.stdout = stdout

	slog.Info("Watching journalctl", "unit", cfg.unit)

	go s.readJournalctl(ctx, ws, cfg.unit)

	return nil
}

func (s *Service) readJournalctl(ctx context.Context, ws *watchedSource, unit string) {
	scanner := bufio.NewScanner(ws.stdout)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxLineBytes)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		_, stop := s.parseAndDispatch(ctx, ws, line, "journalctl:"+unit)
		if stop {
			return
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("Journalctl scanner error", "unit", unit, "error", err)
	}
}

func extractJournalMessage(line string) (string, error) {
	var data map[string]any
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return "", err
	}
	msg, ok := data["MESSAGE"]
	if !ok {
		return "", fmt.Errorf("missing MESSAGE field")
	}
	return fmt.Sprintf("%v", msg), nil
}

func (ws *watchedSource) isStartupEntry(entry types.LogEntry) bool {
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

		if ws.file == nil {
			if err := s.openFile(ws, cfg); err != nil {
				continue
			}
		}
		s.readNewLines(ctx, ws, cfg.path)
	}
}

func (s *Service) readNewLines(ctx context.Context, ws *watchedSource, path string) {
	info, err := ws.file.Stat()
	if err != nil {
		slog.Warn("Failed to stat file", "path", path, "error", err)
		s.reopenFile(ws, path)
		return
	}

	if info.Size() < ws.offset {
		slog.Info("File truncated, reopening", "path", path)
		s.reopenFile(ws, path)
		return
	}

	scanner := bufio.NewScanner(ws.file)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, maxLineBytes)
	count := 0

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		dispatched, stop := s.parseAndDispatch(ctx, ws, line, path)
		if stop {
			return
		}
		if !dispatched {
			continue
		}

		count++
		if count >= maxBatchSize {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("Scanner error", "path", path, "error", err)
	}

	newOffset, _ := ws.file.Seek(0, io.SeekCurrent)
	ws.offset = newOffset
}

// parseAndDispatch is the single parse path for all sources.
// Returns (dispatched, stop). stop=true means caller should terminate loop.
func (s *Service) parseAndDispatch(ctx context.Context, ws *watchedSource, line, source string) (bool, bool) {
	entry, err := s.parseLine(ws, line)
	if err != nil {
		slog.Debug("Failed to parse line", "app", ws.appName, "source", source, "error", err, "line", line)
		return false, false
	}

	isStartup := ws.isStartupEntry(entry)
	if isStartup {
		slog.Info("Detected app startup", "app", ws.appName, "message", entry.Message)
	}

	select {
	case s.LogChan <- AppLogEntry{AppName: ws.appName, Entry: entry, IsStartup: isStartup}:
		return true, false
	case <-ctx.Done():
		return false, true
	default:
		slog.Warn("Log channel full, dropping entry")
		return false, false
	}
}

func (s *Service) parseLine(ws *watchedSource, line string) (types.LogEntry, error) {
	entry, err := ws.parser.Parse(line)
	if err == nil {
		return entry, nil
	}

	// For journalctl source, non-journal parsers can parse raw MESSAGE payload.
	if ws.source.sourceType() == sourceTypeJournalctl {
		raw, extractErr := extractJournalMessage(line)
		if extractErr == nil {
			return ws.parser.Parse(raw)
		}
	}

	return types.LogEntry{}, err
}

func (s *Service) reopenFile(ws *watchedSource, path string) {
	if ws.file != nil {
		ws.file.Close()
		ws.file = nil
	}

	file, err := os.Open(path)
	if err != nil {
		slog.Warn("Failed to reopen file", "path", path, "error", err)
		return
	}

	ws.file = file
	ws.offset = 0
}

func (s *Service) closeAll() {
	for _, ws := range s.sources {
		switch ws.source.sourceType() {
		case sourceTypeFile:
			if ws.file != nil {
				ws.file.Close()
				ws.file = nil
			}
		case sourceTypeJournalctl:
			if ws.cmd != nil && ws.cmd.Process != nil {
				ws.cmd.Process.Kill()
			}
			if ws.stdout != nil {
				ws.stdout.Close()
			}
		}
	}
	close(s.LogChan)
}
