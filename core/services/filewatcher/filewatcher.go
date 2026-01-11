// TODO: Rename to watchdog, cause it's uses not only files
package filewatcher

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"time"

	"github.com/ai-shift/tracer/config"
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

type AppLogEntry struct {
	AppName   string
	Entry     logparser.LogEntry
	IsStartup bool // true if this entry matches the startup pattern
}

// TODO: Instead of flat config with conditional field for different resources
// use any type and create struct config per source
// In the code use type safe check to determine what you've got
type watchedSource struct {
	sourceType sourceType
	appName    string
	path       string
	unit       string // for journalctl
	file       *os.File
	parser     logparser.Parser
	offset     int64

	// startup detection
	startupRegex *regexp.Regexp

	// journalctl specific
	cmd    *exec.Cmd
	stdout io.ReadCloser
}

type Service struct {
	LogChan chan AppLogEntry
	sources []*watchedSource
}

func New(apps []config.App) (*Service, error) {
	s := &Service{
		LogChan: make(chan AppLogEntry, 1000),
		sources: make([]*watchedSource, 0, len(apps)),
	}

	for _, app := range apps {
		parser, err := logparser.New(app)
		if err != nil {
			return nil, err
		}

		ws := &watchedSource{
			appName: app.Name,
			parser:  parser,
		}

		// Compile startup pattern if specified
		if app.StartupPattern != "" {
			re, err := regexp.Compile(app.StartupPattern)
			if err != nil {
				slog.Warn("Invalid startup pattern, ignoring", "app", app.Name, "pattern", app.StartupPattern, "error", err)
			} else {
				ws.startupRegex = re
				slog.Info("Startup detection enabled", "app", app.Name, "pattern", app.StartupPattern)
			}
		}

		if app.Format == "journalctl" {
			ws.sourceType = sourceTypeJournalctl
			ws.unit = app.Unit
			ws.path = "journalctl:" + app.Unit
		} else {
			ws.sourceType = sourceTypeFile
			ws.path = app.Path
		}

		s.sources = append(s.sources, ws)
	}

	return s, nil
}

func (s *Service) GetAppNames() []string {
	names := make([]string, 0, len(s.sources))
	for _, ws := range s.sources {
		names = append(names, ws.appName)
	}
	return names
}

func (s *Service) Start(ctx context.Context) {
	// Start all sources
	for _, ws := range s.sources {
		switch ws.sourceType {
		case sourceTypeFile:
			if err := s.openFile(ws); err != nil {
				slog.Error("Failed to open file", "path", ws.path, "error", err)
			}
		case sourceTypeJournalctl:
			if err := s.startJournalctl(ctx, ws); err != nil {
				slog.Error("Failed to start journalctl", "unit", ws.unit, "error", err)
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

func (s *Service) openFile(ws *watchedSource) error {
	file, err := os.Open(ws.path)
	if err != nil {
		return err
	}

	// Seek to end to only read new lines
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		file.Close()
		return err
	}

	ws.file = file
	ws.offset = offset
	slog.Info("Watching file", "path", ws.path, "offset", offset)
	return nil
}

func (s *Service) startJournalctl(ctx context.Context, ws *watchedSource) error {
	// Start journalctl in follow mode with JSON output
	// --since "now" ensures we only get new entries
	cmd := exec.CommandContext(ctx, "journalctl",
		"-u", ws.unit,
		"-f",         // follow mode
		"-o", "json", // JSON output
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

	slog.Info("Watching journalctl", "unit", ws.unit)

	// Start a goroutine to read from journalctl
	go s.readJournalctl(ctx, ws)

	return nil
}

func (s *Service) readJournalctl(ctx context.Context, ws *watchedSource) {
	scanner := bufio.NewScanner(ws.stdout)
	// Increase buffer size for potentially large log lines
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
			slog.Error("Failed to parse journalctl line", "error", err, "unit", ws.unit, "line", line)
			continue
		}

		// Check if this is a startup message
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
		slog.Warn("Journalctl scanner error", "unit", ws.unit, "error", err)
	}
}

// isStartupEntry checks if the log entry matches the startup pattern
func (ws *watchedSource) isStartupEntry(entry logparser.LogEntry) bool {
	if ws.startupRegex == nil {
		return false
	}
	return ws.startupRegex.MatchString(entry.Message)
}

func (s *Service) pollAll(ctx context.Context) {
	for _, ws := range s.sources {
		// Only poll file sources; journalctl is handled by goroutine
		if ws.sourceType != sourceTypeFile {
			continue
		}

		if ws.file == nil {
			// Try to reopen
			if err := s.openFile(ws); err != nil {
				continue
			}
		}
		s.readNewLines(ctx, ws)
	}
}

func (s *Service) readNewLines(ctx context.Context, ws *watchedSource) {
	// Check if file was truncated or rotated
	info, err := ws.file.Stat()
	if err != nil {
		slog.Warn("Failed to stat file", "path", ws.path, "error", err)
		s.reopenFile(ws)
		return
	}

	if info.Size() < ws.offset {
		// File was truncated, reopen
		slog.Info("File truncated, reopening", "path", ws.path)
		s.reopenFile(ws)
		return
	}

	// Read new lines
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

		// Check if this is a startup message
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
		slog.Warn("Scanner error", "path", ws.path, "error", err)
	}

	// Update offset
	newOffset, _ := ws.file.Seek(0, io.SeekCurrent)
	ws.offset = newOffset
}

func (s *Service) reopenFile(ws *watchedSource) {
	if ws.file != nil {
		ws.file.Close()
		ws.file = nil
	}

	file, err := os.Open(ws.path)
	if err != nil {
		slog.Warn("Failed to reopen file", "path", ws.path, "error", err)
		return
	}

	ws.file = file
	ws.offset = 0
}

func (s *Service) closeAll() {
	for _, ws := range s.sources {
		switch ws.sourceType {
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
