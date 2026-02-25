package source

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// JournalctlConfig contains configuration for journalctl source.
type JournalctlConfig struct {
	Unit string
}

// journalctlSource reads from systemd journal.
type journalctlSource struct {
	unit    string
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	scanner *bufio.Scanner
}

// NewJournalctl creates a new journalctl source.
// Enforces creation of the error channel for health monitoring.
func NewJournalctl(cfg JournalctlConfig) Source {
	return &journalctlSource{
		unit: cfg.Unit,
	}
}

func (js *journalctlSource) Open(ctx context.Context) error {
	if strings.TrimSpace(js.unit) == "" {
		return fmt.Errorf("journalctl source: empty unit")
	}

	// Start from "now" (no historical backfill).
	cmd := exec.CommandContext(ctx, "journalctl", "-u", js.unit, "-f", "-n", "0", "-o", "cat")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("journalctl stdout pipe (%s): %w", js.unit, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("journalctl start (%s): %w", js.unit, err)
	}

	scanner := bufio.NewScanner(stdout)
	const maxLineBytes = 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	js.cmd = cmd
	js.stdout = stdout
	js.scanner = scanner
	return nil
}

func (js *journalctlSource) Next(ctx context.Context) (string, error) {
	if js.scanner == nil {
		return "", fmt.Errorf("journalctl source not open")
	}

	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if js.scanner.Scan() {
			line := strings.TrimSpace(js.scanner.Text())
			if line == "" {
				continue
			}
			return line, nil
		}

		if err := js.scanner.Err(); err != nil {
			return "", fmt.Errorf("journalctl scanner (%s): %w", js.unit, err)
		}
		return "", io.EOF
	}
}

func (js *journalctlSource) Close() error {
	var closeErr error
	if js.stdout != nil {
		if err := js.stdout.Close(); err != nil {
			closeErr = err
		}
		js.stdout = nil
	}
	if js.cmd != nil {
		if js.cmd.Process != nil {
			_ = js.cmd.Process.Kill()
		}
		if err := js.cmd.Wait(); err != nil && closeErr == nil {
			closeErr = err
		}
		js.cmd = nil
	}
	js.scanner = nil
	return closeErr
}
