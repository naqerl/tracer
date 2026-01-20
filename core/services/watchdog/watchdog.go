package watchdog

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"time"

	collectorconfig "github.com/ai-shift/tracer/config/collector"
	"github.com/ai-shift/tracer/core/services/logparser"
)

const (
	pollInterval = 100 * time.Millisecond
	maxBatchSize = 100
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
	path string
}

func (c fileSourceConfig) sourceType() sourceType {
	return sourceTypeFile
}

func (c fileSourceConfig) label() string {
	return c.path
}

type journalctlSourceConfig struct {
	unit string
}

func (c journalctlSourceConfig) sourceType() sourceType {
	return sourceTypeJournalctl
}

func (c journalctlSourceConfig) label() string {
	return "journalctl:" + c.unit
}

type AppLogEntry struct {
	AppName   string
	Entry     logparser.LogEntry
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

func New(apps []collectorconfig.App) (*Service, error) {
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

func buildSourceConfig(app collectorconfig.App) (sourceConfig, error) {
	if app.Format == "journalctl" {
		return journalctlSourceConfig{unit: app.Unit}, nil
	}

	return fileSourceConfig{path: app.Path}, nil
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

	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		file.Close()
		return err
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
		"--since", "now",
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
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		entry, err := ws.parser.Parse(line)
		if err != nil {
			slog.Error("Failed to parse journalctl line", "error", err, "unit", unit, "line", line)
			continue
		}

		isStartup := ws.isStartupEntry(entry)
		if isStartup {
			slog.Info("Detected app startup", "app", ws.appName, "message", entry.Message)
		}

		select {
		case s.LogChan <- AppLogEntry{AppName: ws.appName, Entry: entry, IsStartup: isStartup}:
		case <-ctx.Done():
			return
		default:
			slog.Warn("Log channel full, dropping entry")
		}
	}

	if err := scanner.Err(); err != nil {
		slog.Warn("Journalctl scanner error", "unit", unit, "error", err)
	}
}

func (ws *watchedSource) isStartupEntry(entry logparser.LogEntry) bool {
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
	count := 0

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		entry, err := ws.parser.Parse(line)
		if err != nil {
			slog.Debug("Failed to parse line", "error", err, "line", line)
			continue
		}

		isStartup := ws.isStartupEntry(entry)
		if isStartup {
			slog.Info("Detected app startup", "app", ws.appName, "message", entry.Message)
		}

		select {
		case s.LogChan <- AppLogEntry{AppName: ws.appName, Entry: entry, IsStartup: isStartup}:
		default:
			slog.Warn("Log channel full, dropping entry")
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
