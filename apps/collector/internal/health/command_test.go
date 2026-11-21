package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"tracer/libs/types"
)

func commandPoller(t *testing.T, params map[string]string) Poller {
	t.Helper()
	poller, err := New(types.HealthCheckSpec{Type: "command", Params: params})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return poller
}

func firstResult(t *testing.T, poller Poller) Result {
	t.Helper()
	select {
	case result := <-poller.Produce(t.Context()):
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("no health result")
		return Result{}
	}
}

func TestCommandExitsZeroIsUp(t *testing.T) {
	poller := commandPoller(t, map[string]string{"command": "exit 0", "interval": "1h"})
	if got := firstResult(t, poller).Status; got != types.HealthStatusUp {
		t.Fatalf("status = %q, want up", got)
	}
}

func TestCommandNonZeroIsDown(t *testing.T) {
	poller := commandPoller(t, map[string]string{"command": "echo nope >&2; exit 7", "interval": "1h"})
	if got := firstResult(t, poller).Status; got != types.HealthStatusDown {
		t.Fatalf("status = %q, want down", got)
	}
}

func TestCommandExpectedExitAcceptsList(t *testing.T) {
	for _, code := range []string{"2", "0,2", "0, 2, 3"} {
		t.Run(code, func(t *testing.T) {
			poller := commandPoller(t, map[string]string{
				"command":       "exit 2",
				"expected_exit": code,
				"interval":      "1h",
			})
			if got := firstResult(t, poller).Status; got != types.HealthStatusUp {
				t.Fatalf("status = %q, want up for expected_exit %q", got, code)
			}
		})
	}
}

func TestCommandExpectMatchesMultilineOutput(t *testing.T) {
	poller := commandPoller(t, map[string]string{
		"command":  "printf 'starting\\nready\\n'",
		"expect":   "^ready$",
		"interval": "1h",
	})
	if got := firstResult(t, poller).Status; got != types.HealthStatusUp {
		t.Fatalf("status = %q, want up", got)
	}

	poller = commandPoller(t, map[string]string{
		"command":  "printf 'starting\\nready\\n'",
		"expect":   "^missing$",
		"interval": "1h",
	})
	if got := firstResult(t, poller).Status; got != types.HealthStatusDown {
		t.Fatalf("status = %q, want down", got)
	}
}

func TestCommandTimeoutKillsProcessGroup(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	poller := commandPoller(t, map[string]string{
		"command":  fmt.Sprintf("sleep 30 & echo $! > %s; wait", pidFile),
		"timeout":  "150ms",
		"interval": "1h",
	})
	started := time.Now()
	if got := firstResult(t, poller).Status; got != types.HealthStatusDown {
		t.Fatalf("status = %q, want down", got)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("timeout took %s, want it to return promptly", elapsed)
	}

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if childExited(pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived the timeout: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCommandBadSpecFailsNew(t *testing.T) {
	if _, err := New(types.HealthCheckSpec{Type: "command", Params: map[string]string{}}); err == nil {
		t.Fatal("expected a command without command to fail")
	} else if !strings.Contains(err.Error(), "command health:") {
		t.Fatalf("error = %v, want it to start with command health", err)
	}

	if _, err := New(types.HealthCheckSpec{Type: "command", Params: map[string]string{
		"command": "true",
		"expect":  "(",
	}}); err == nil {
		t.Fatal("expected an invalid expect regexp to fail")
	} else if !strings.Contains(err.Error(), "command health:") {
		t.Fatalf("error = %v, want it to start with command health", err)
	}
}

func TestCommandFailureLogsOneWarnWithOutputTail(t *testing.T) {
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	poller := commandPoller(t, map[string]string{
		"command":  "printf '%*s' 300 '' | tr ' ' a; printf TAIL; exit 1",
		"interval": "1h",
	})
	if got := firstResult(t, poller).Status; got != types.HealthStatusDown {
		t.Fatalf("status = %q, want down", got)
	}

	text := logs.String()
	if count := strings.Count(text, "health check failed"); count != 1 {
		t.Fatalf("failure log count = %d, want 1: %s", count, text)
	}
	if strings.Contains(text, strings.Repeat("a", 201)) {
		t.Fatalf("log carries more than the output tail: %s", text)
	}
	if !strings.Contains(text, "TAIL") {
		t.Fatalf("log does not carry the output tail: %s", text)
	}
}

func TestCommandCancelledContextDoesNotLog(t *testing.T) {
	var logs strings.Builder
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	poller := commandPoller(t, map[string]string{"command": "sleep 5", "interval": "1h"})
	ctx, cancel := context.WithCancel(t.Context())
	results := poller.Produce(ctx)
	time.Sleep(50 * time.Millisecond)
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, open := <-results:
			if !open {
				if text := logs.String(); strings.Contains(text, "health check failed") {
					t.Fatalf("cancelled check must not log a failure: %s", text)
				}
				return
			}
		case <-deadline:
			t.Fatal("poller channel did not close after cancel")
		}
	}
}

func childExited(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return true
	}
	fields := strings.Fields(string(raw))
	return len(fields) > 2 && fields[2] == "Z"
}
