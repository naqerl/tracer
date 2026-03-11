package source

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
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
	scanner *bufio.Scanner
	ctx     context.Context // stored from Open for use in restart
	seeked  bool

	// linesFromEnd specifies how many lines to show from the end.
	// 0 means start from now (tail mode).
	// -1 means show all (from beginning).
	// N>0 means show last N lines.
	linesFromEnd int64
}

// NewJournalctl creates a new journalctl source.
// Enforces creation of the error channel for health monitoring.
func NewJournalctl(cfg JournalctlConfig) *journalctlSource {
	return &journalctlSource{
		unit:         cfg.Unit,
		linesFromEnd: 0, // Default: start from now
	}
}

func (js *journalctlSource) Open(ctx context.Context) error {
	if strings.TrimSpace(js.unit) == "" {
		return errors.New("journalctl source: empty unit")
	}
	js.ctx = ctx

	cmd := exec.CommandContext(ctx, "systemctl", "list-unit-files", js.unit)
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return errors.New("unit not found")
		}
		return err
	}

	return nil
}

func (js *journalctlSource) Next(ctx context.Context) (string, error) {
	if js.seeked {
		if err := js.Close(); err != nil {
			return "", err
		}
	}

	if js.cmd == nil {
		if err := js.startJournalCtl(); err != nil {
			return "", err
		}
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
	if err := js.cmd.Process.Kill(); err != nil {
		return err
	}
	if err := js.cmd.Wait(); err != nil {
		return err
	}

	js.cmd = nil
	js.scanner = nil
	js.seeked = false

	return nil
}

// Seek implements io.Seeker for journalctlSource.
// Since journalctl doesn't support runtime seeking, this method kills the
// current process and restarts it with appropriate flags.
//
// Position semantics:
//   - Seek(0, io.SeekStart): Read from beginning (all history)
//   - Seek(N, io.SeekStart): Not supported (journalctl can't skip first N lines)
//   - Seek(0, io.SeekEnd): Read from now (tail mode)
//   - Seek(-N, io.SeekEnd): Read last N lines, then follow
//   - Seek(0, io.SeekCurrent): No-op, returns 0
//
// Returns 0 for all successful seeks (positions are opaque).
func (js *journalctlSource) Seek(offset int64, whence int) (int64, error) {
	if js.ctx == nil {
		return 0, errors.New("journalctl source not open")
	}

	switch whence {
	case io.SeekStart:
		if offset != 0 {
			return 0, fmt.Errorf("journalctl source: SeekStart only supports offset=0, got %d", offset)
		}
		js.linesFromEnd = -1 // From beginning
	case io.SeekEnd:
		if offset > 0 {
			return 0, errors.New("journalctl source: SeekEnd only supports offset<=0")
		}
		if offset == 0 {
			js.linesFromEnd = 0 // From now
		} else {
			// offset is negative, so -offset is positive
			js.linesFromEnd = -offset // Last N lines
		}
	case io.SeekCurrent:
		if offset != 0 {
			return 0, errors.New("journalctl source: SeekCurrent only supports offset=0 (query position)")
		}
		// No position change
		return 0, nil
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}

	js.seeked = true
	return 0, nil
}

func (js *journalctlSource) startJournalCtl() error {
	// Build command based on current mode
	flags := buildFlags(js.unit, js.linesFromEnd)
	cmd := exec.CommandContext(js.ctx, "journalctl", flags...)
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
	js.scanner = scanner

	return nil
}

// buildFlags constructs journalctl flags based on current mode.
func buildFlags(unit string, linesFromEnd int64) []string {
	flags := []string{"-u", unit, "-o", "cat", "_TRANSPORT=stdout", "-f"}

	switch linesFromEnd {
	case -1:
		// From beginning: -f (no -n flag, shows all)
		// flags already set above
	case 0:
		// From now: -f -n 0
		flags = append(flags, "-n", "0")
	default:
		// Last N lines: -f -n N
		flags = append(flags, "-n", strconv.FormatInt(linesFromEnd, 10))
	}

	return flags
}
