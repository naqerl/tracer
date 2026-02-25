package source

import (
	"bufio"
	"context"
	"log/slog"
	"os/exec"
	"strings"
)

// journalctlSource reads from systemd journal.
type journalctlSource struct {
	app  string
	unit string
}

// NewJournalctl creates a new journalctl source.
// Enforces creation of the error channel for health monitoring.
func NewJournalctl(cfg JournalctlConfig) Source {
	return &journalctlSource{
		app:  cfg.AppName,
		unit: cfg.Unit,
	}
}

func (js *journalctlSource) Lines(ctx context.Context) <-chan string {
	out := make(chan string)
	go js.readLoop(ctx, out)
	return out
}

func (js *journalctlSource) readLoop(ctx context.Context, out chan<- string) {
	defer close(out)

	if strings.TrimSpace(js.unit) == "" {
		slog.Error("journalctl source unit is empty", "app", js.app)
		return
	}

	// Start from "now" (no historical backfill).
	cmd := exec.CommandContext(ctx, "journalctl", "-u", js.unit, "-f", "-n", "0", "-o", "cat")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		slog.Error("journalctl stdout pipe failed", "app", js.app, "unit", js.unit, "error", err)
		return
	}

	if err := cmd.Start(); err != nil {
		slog.Error("journalctl start failed", "app", js.app, "unit", js.unit, "error", err)
		return
	}
	defer func() {
		_ = cmd.Wait()
	}()

	scanner := bufio.NewScanner(stdout)
	const maxLineBytes = 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		select {
		case out <- line:
		case <-ctx.Done():
			return
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		slog.Error("journalctl scanner failed", "app", js.app, "unit", js.unit, "error", err)
	}
}
