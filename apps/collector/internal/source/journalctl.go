package source

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/ai-shift/tracer/libs/types"
)

// journalctlSource reads from systemd journal.
type journalctlSource struct {
	app   string
	unit  string
	since string
	cmd   *exec.Cmd
	errCh chan error
	mu    sync.Mutex
}

// NewJournalctl creates a new journalctl source.
// Enforces creation of the error channel for health monitoring.
func NewJournalctl(cfg JournalctlConfig) Source {
	return &journalctlSource{
		app:   cfg.AppName,
		unit:  cfg.Unit,
		since: cfg.Since,
		errCh: make(chan error, 1),
	}
}

func (js *journalctlSource) Name() string    { return "journalctl:" + js.unit }
func (js *journalctlSource) AppName() string { return js.app }

// Health returns the error channel. Read loop sends errors here.
func (js *journalctlSource) Health() <-chan error {
	return js.errCh
}

func (js *journalctlSource) sendError(err error) {
	select {
	case js.errCh <- err:
	default:
		// Channel full or no receiver, drop
	}
}

func (js *journalctlSource) Read(ctx context.Context) <-chan types.RawLine {
	out := make(chan types.RawLine)
	go js.readLoop(ctx, out)
	return out
}

func (js *journalctlSource) readLoop(ctx context.Context, out chan<- types.RawLine) {
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

func (js *journalctlSource) runOnce(ctx context.Context, out chan<- types.RawLine) error {
	js.mu.Lock()
	js.cmd = exec.CommandContext(ctx, "journalctl",
		"-u", js.unit,
		"-f",
		"-o", "json",
		"--since", js.since,
	)
	cmd := js.cmd
	js.mu.Unlock()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		js.sendError(fmt.Errorf("journalctl stdout pipe: %w", err))
		return err
	}

	if err := cmd.Start(); err != nil {
		js.sendError(fmt.Errorf("journalctl start: %w", err))
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

		// Extract MESSAGE from journal JSON envelope
		msg := extractMessage(line)
		if msg == "" {
			continue
		}

		select {
		case out <- types.RawLine{App: js.app, Line: msg}:
		case <-ctx.Done():
			stdout.Close()
			cmd.Wait()
			return ctx.Err()
		}
	}

	stdout.Close()

	// Check for scanner errors
	if err := scanner.Err(); err != nil {
		js.sendError(fmt.Errorf("journalctl scanner: %w", err))
		return err
	}

	// Wait for process and report errors
	if err := cmd.Wait(); err != nil {
		js.sendError(fmt.Errorf("journalctl exited: %w", err))
		return err
	}
	return nil
}

// Restart kills and restarts journalctl.
func (js *journalctlSource) Restart() error {
	js.mu.Lock()
	cmd := js.cmd
	js.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		cmd.Process.Kill()
		cmd.Wait()
	}
	return nil
}

// extractMessage extracts the MESSAGE field from journalctl JSON output.
func extractMessage(line string) string {
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
