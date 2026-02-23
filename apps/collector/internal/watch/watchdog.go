package watchdog

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-shift/tracer/libs/types"
)

const (
	pollInterval = 100 * time.Millisecond
	maxBatchSize = 100
	maxLineBytes = 1024 * 1024
	readFromEnd  = "end"
)

// Source represents a log source (file or journalctl).
type Source interface {
	Read(ctx context.Context) <-chan types.RawLine
	Name() string
	AppName() string
}

// Service manages multiple log sources.
type Service struct {
	sources []Source
}

// New creates a watchdog service for the given apps.
func New(configs []SourceConfig) (*Service, error) {
	s := &Service{
		sources: make([]Source, 0, len(configs)),
	}

	for _, cfg := range configs {
		var source Source
		switch cfg.Kind {
		case "file":
			source = &fileSource{
				appName:  cfg.AppName,
				path:     cfg.Path,
				readFrom: cfg.ReadFrom,
			}
		case "journalctl":
			source = &journalctlSource{
				appName: cfg.AppName,
				unit:    cfg.Unit,
				since:   cfg.Since,
			}
		default:
			panic(fmt.Sprintf("watchdog: unknown source kind %q for app %s", cfg.Kind, cfg.AppName))
		}

		s.sources = append(s.sources, source)
	}

	return s, nil
}

// Start begins watching all sources and emits raw lines to the output channel.
func (s *Service) Start(ctx context.Context, output chan<- types.RawLine) {
	var wg sync.WaitGroup
	for _, source := range s.sources {
		wg.Go(func() {
			for line := range source.Read(ctx) {
				select {
				case output <- line:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	wg.Wait()
}

// SourceNames returns the names of all sources.
func (s *Service) SourceNames() []string {
	names := make([]string, len(s.sources))
	for i, src := range s.sources {
		names[i] = src.Name()
	}
	return names
}

// AppNames returns the app names of all sources.
func (s *Service) AppNames() []string {
	names := make([]string, len(s.sources))
	for i, src := range s.sources {
		names[i] = src.AppName()
	}
	return names
}

// SourceConfig configures a log source.
type SourceConfig struct {
	AppName  string
	Kind     string // "file" or "journalctl"
	Path     string // for file
	ReadFrom string // for file: "start" or "end"
	Unit     string // for journalctl
	Since    string // for journalctl
}

// fileSource reads from a log file.
type fileSource struct {
	appName  string
	path     string
	readFrom string
	file     *os.File
	offset   atomic.Int64
	mu       sync.Mutex
}

func (fs *fileSource) Name() string    { return fs.path }
func (fs *fileSource) AppName() string { return fs.appName }

func (fs *fileSource) Read(ctx context.Context) <-chan types.RawLine {
	out := make(chan types.RawLine)
	go fs.readLoop(ctx, out)
	return out
}

func (fs *fileSource) readLoop(ctx context.Context, out chan<- types.RawLine) {
	defer close(out)

	if err := fs.open(); err != nil {
		return
	}
	defer fs.close()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	buf := make([]byte, 0, 64*1024)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := fs.readBatch(out, buf); err != nil {
				return
			}
		}
	}
}

func (fs *fileSource) open() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	file, err := os.Open(fs.path)
	if err != nil {
		return err
	}

	offset := int64(0)
	if fs.readFrom == readFromEnd {
		offset, err = file.Seek(0, io.SeekEnd)
		if err != nil {
			file.Close()
			return err
		}
	}

	fs.file = file
	fs.offset.Store(offset)
	return nil
}

func (fs *fileSource) readBatch(out chan<- types.RawLine, buf []byte) error {
	fs.mu.Lock()
	file := fs.file
	fs.mu.Unlock()

	if file == nil {
		if err := fs.open(); err != nil {
			return err
		}
		file = fs.file
	}

	info, err := file.Stat()
	if err != nil {
		fs.reopen()
		return nil
	}

	fs.mu.Lock()
	currentOffset := fs.offset.Load()
	fs.mu.Unlock()

	if info.Size() < currentOffset {
		fs.reopen()
		return nil
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(buf, maxLineBytes)

	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			select {
			case out <- types.RawLine{App: fs.appName, Line: line}:
			default:
				// Channel full, drop
			}
		}
		count++
		if count >= maxBatchSize {
			break
		}
	}

	newOffset, _ := file.Seek(0, io.SeekCurrent)
	fs.offset.Store(newOffset)

	return nil
}

func (fs *fileSource) reopen() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.file != nil {
		fs.file.Close()
		fs.file = nil
	}
	fs.offset.Store(0)
}

func (fs *fileSource) close() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.file != nil {
		fs.file.Close()
		fs.file = nil
	}
}

// journalctlSource reads from systemd journal.
type journalctlSource struct {
	appName string
	unit    string
	since   string
}

func (js *journalctlSource) Name() string    { return "journalctl:" + js.unit }
func (js *journalctlSource) AppName() string { return js.appName }

func (js *journalctlSource) Read(ctx context.Context) <-chan types.RawLine {
	out := make(chan types.RawLine)
	go js.readLoop(ctx, out)
	return out
}

func (js *journalctlSource) readLoop(ctx context.Context, out chan<- types.RawLine) {
	defer close(out)

	cmd := exec.CommandContext(ctx, "journalctl",
		"-u", js.unit,
		"-f",
		"-o", "json",
		"--since", js.since,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}

	if err := cmd.Start(); err != nil {
		return
	}

	defer cmd.Wait()
	defer stdout.Close()

	buf := make([]byte, 0, 64*1024)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(buf, maxLineBytes)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()
		if line == "" {
			continue
		}

		// Extract MESSAGE from journal JSON
		msg := extractJournalMessage(line)
		if msg != "" {
			select {
			case out <- types.RawLine{App: js.appName, Line: msg}:
			default:
				// Channel full, drop
			}
		}
	}
}

func extractJournalMessage(line string) string {
	// Quick check to avoid JSON parsing overhead
	if len(line) < 10 || line[0] != '{' {
		return ""
	}

	var data struct {
		Message string `json:"MESSAGE"`
	}
	if err := json.Unmarshal([]byte(line), &data); err != nil {
		return ""
	}
	return data.Message
}
