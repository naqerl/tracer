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
	Health(ctx context.Context) <-chan error
	Name() string
	AppName() string
	Restart() error
}

// Service manages multiple log sources.
type Service struct {
	sources []Source
}

// New creates a watchdog service for the given sources.
func New(sources []Source) *Service {
	return &Service{sources: sources}
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

// FileSource reads from a log file.
type FileSource struct {
	App    string
	Path   string
	From   string // "start" or "end"
	file   *os.File
	offset atomic.Int64
	mu     sync.Mutex
}

func (fs *FileSource) Name() string    { return fs.Path }
func (fs *FileSource) AppName() string { return fs.App }

// Health returns a channel that reports errors when the source becomes unhealthy.
func (fs *FileSource) Health(ctx context.Context) <-chan error {
	ch := make(chan error, 1)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := fs.checkHealth(); err != nil {
					select {
					case ch <- err:
						return // Signal once and exit
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return ch
}

func (fs *FileSource) checkHealth() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.file == nil {
		return fmt.Errorf("file not open")
	}
	_, err := fs.file.Stat()
	return err
}

// Restart closes and reopens the file.
func (fs *FileSource) Restart() error {
	fs.close()
	return fs.open()
}

func (fs *FileSource) Read(ctx context.Context) <-chan types.RawLine {
	out := make(chan types.RawLine)
	go fs.readLoop(ctx, out)
	return out
}

func (fs *FileSource) readLoop(ctx context.Context, out chan<- types.RawLine) {
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

func (fs *FileSource) open() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	file, err := os.Open(fs.Path)
	if err != nil {
		return err
	}

	offset := int64(0)
	if fs.From == readFromEnd {
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

func (fs *FileSource) readBatch(out chan<- types.RawLine, buf []byte) error {
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
			case out <- types.RawLine{App: fs.App, Line: line}:
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

func (fs *FileSource) reopen() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.file != nil {
		fs.file.Close()
		fs.file = nil
	}
	fs.offset.Store(0)
}

func (fs *FileSource) close() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.file != nil {
		fs.file.Close()
		fs.file = nil
	}
}

// JournalctlSource reads from systemd journal.
type JournalctlSource struct {
	App   string
	Unit  string
	Since string
	cmd   *exec.Cmd
	mu    sync.Mutex
}

func (js *JournalctlSource) Name() string    { return "journalctl:" + js.Unit }
func (js *JournalctlSource) AppName() string { return js.App }

// Health returns a channel that reports errors when the process dies.
func (js *JournalctlSource) Health(ctx context.Context) <-chan error {
	ch := make(chan error, 1)
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			js.mu.Lock()
			cmd := js.cmd
			js.mu.Unlock()

			if cmd == nil || cmd.Process == nil {
				select {
				case ch <- fmt.Errorf("journalctl not running"):
					return
				case <-ctx.Done():
					return
				}
			}

			// Wait for process to exit
			err := cmd.Wait()
			if err != nil {
				select {
				case ch <- fmt.Errorf("journalctl exited: %w", err):
					return
				case <-ctx.Done():
					return
				}
			}

			// Process exited cleanly, check if context is done
			select {
			case <-ctx.Done():
				return
			default:
				// Process exited but context still active - this is an error
				select {
				case ch <- fmt.Errorf("journalctl exited unexpectedly"):
					return
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch
}

func (js *JournalctlSource) Read(ctx context.Context) <-chan types.RawLine {
	out := make(chan types.RawLine)
	go js.readLoop(ctx, out)
	return out
}

func (js *JournalctlSource) readLoop(ctx context.Context, out chan<- types.RawLine) {
	defer close(out)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := js.runOnce(ctx, out)
		if err != nil {
			// Exit on context cancellation, otherwise retry
			if ctx.Err() != nil {
				return
			}
			// Brief pause before retry
			select {
			case <-time.After(time.Second):
				continue
			case <-ctx.Done():
				return
			}
		}
		return
	}
}

func (js *JournalctlSource) runOnce(ctx context.Context, out chan<- types.RawLine) error {
	js.mu.Lock()
	js.cmd = exec.CommandContext(ctx, "journalctl",
		"-u", js.Unit,
		"-f",
		"-o", "json",
		"--since", js.Since,
	)
	cmd := js.cmd
	js.mu.Unlock()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	buf := make([]byte, 0, 64*1024)
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(buf, maxLineBytes)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			stdout.Close()
			cmd.Wait()
			return ctx.Err()
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
			case out <- types.RawLine{App: js.App, Line: msg}:
			case <-ctx.Done():
				stdout.Close()
				cmd.Wait()
				return ctx.Err()
			}
		}
	}

	stdout.Close()
	return cmd.Wait()
}

// Restart kills and restarts journalctl.
func (js *JournalctlSource) Restart() error {
	js.mu.Lock()
	cmd := js.cmd
	js.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
		cmd.Wait()
	}
	return nil
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
