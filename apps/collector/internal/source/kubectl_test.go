package source

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewKubectl(t *testing.T) {
	src := NewKubectl(KubectlConfig{
		Resource:   "deployment/myapp",
		Namespace:  "default",
		Container:  "",
		Kubeconfig: "",
	})
	if src == nil {
		t.Fatal("NewKubectl returned nil")
	}
	if src.resource != "deployment/myapp" {
		t.Fatalf("unexpected resource: %q", src.resource)
	}
	if src.namespace != "default" {
		t.Fatalf("unexpected namespace: %q", src.namespace)
	}
}

func TestKubectlSourceOpenValid(t *testing.T) {
	src := NewKubectl(KubectlConfig{
		Resource:  "deployment/myapp",
		Namespace: "flux-system",
	})
	if err := src.Open(t.Context()); err != nil {
		t.Fatalf("Open failed: %v", err)
	}
}

func TestKubectlSourceOpenEmptyResource(t *testing.T) {
	src := NewKubectl(KubectlConfig{
		Resource:  "",
		Namespace: "default",
	})
	if err := src.Open(t.Context()); err == nil {
		t.Fatal("expected error for empty resource")
	}
}

func TestKubectlSourceSeekStart(t *testing.T) {
	src := NewKubectl(KubectlConfig{Resource: "deployment/myapp", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	pos, err := src.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("SeekStart failed: %v", err)
	}
	if pos != 0 {
		t.Fatalf("SeekStart returned non-zero position: %d", pos)
	}
	if !src.seeked {
		t.Fatal("seeked flag not set after SeekStart")
	}
	if src.linesFromEnd != -1 {
		t.Fatalf("linesFromEnd = %d, want -1", src.linesFromEnd)
	}
}

func TestKubectlSourceSeekEnd(t *testing.T) {
	src := NewKubectl(KubectlConfig{Resource: "deployment/myapp", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	// Seek to end (follow from now)
	pos, err := src.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("SeekEnd failed: %v", err)
	}
	if !src.seeked {
		t.Fatal("seeked flag not set after SeekEnd")
	}
	if src.linesFromEnd != 0 {
		t.Fatalf("linesFromEnd = %d, want 0", src.linesFromEnd)
	}
	_ = pos
}

func TestKubectlSourceSeekLastN(t *testing.T) {
	src := NewKubectl(KubectlConfig{Resource: "deployment/myapp", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	// Seek to last 100 lines
	pos, err := src.Seek(-100, io.SeekEnd)
	if err != nil {
		t.Fatalf("SeekEnd -100 failed: %v", err)
	}
	if !src.seeked {
		t.Fatal("seeked flag not set after SeekEnd")
	}
	if src.linesFromEnd != 100 {
		t.Fatalf("linesFromEnd = %d, want 100", src.linesFromEnd)
	}
	_ = pos
}

func TestKubectlSourceSeekCurrent(t *testing.T) {
	src := NewKubectl(KubectlConfig{Resource: "deployment/myapp", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	pos, err := src.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("SeekCurrent failed: %v", err)
	}
	if pos != 0 {
		t.Fatalf("SeekCurrent returned non-zero: %d", pos)
	}
	if src.seeked {
		t.Fatal("seeked flag set after SeekCurrent")
	}
}

func TestKubectlSourceSeekNotOpen(t *testing.T) {
	src := NewKubectl(KubectlConfig{Resource: "deployment/myapp", Namespace: "default"})
	_, err := src.Seek(0, io.SeekStart)
	if err == nil {
		t.Fatal("expected error seeking on unopened source")
	}
}

func TestKubectlSourceSeekInvalidWhence(t *testing.T) {
	src := NewKubectl(KubectlConfig{Resource: "deployment/myapp", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	_, err := src.Seek(0, 99)
	if err == nil {
		t.Fatal("expected error for invalid whence")
	}
}

func TestKubectlSourceCloseNilCmd(t *testing.T) {
	src := NewKubectl(KubectlConfig{Resource: "deployment/myapp", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("Close on nil cmd failed: %v", err)
	}
}

func TestKubectlSourceCloseDoubleClose(t *testing.T) {
	src := NewKubectl(KubectlConfig{Resource: "deployment/myapp", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestKubectlSourceBuildFlags(t *testing.T) {
	tests := []struct {
		name         string
		cfg          KubectlConfig
		follow       bool
		linesFromEnd int64
		wantArgs     []string
	}{
		{
			name:         "follow from now",
			cfg:          KubectlConfig{Resource: "deployment/myapp", Namespace: "default"},
			follow:       true,
			linesFromEnd: 0,
			wantArgs:     []string{"logs", "deployment/myapp", "-n", "default", "-f", "--tail=0"},
		},
		{
			name:         "no follow from now",
			cfg:          KubectlConfig{Resource: "deployment/myapp", Namespace: "default"},
			follow:       false,
			linesFromEnd: 0,
			wantArgs:     []string{"logs", "deployment/myapp", "-n", "default", "--tail=0"},
		},
		{
			name:         "follow last 100 lines",
			cfg:          KubectlConfig{Resource: "deployment/myapp", Namespace: "default"},
			follow:       true,
			linesFromEnd: 100,
			wantArgs:     []string{"logs", "deployment/myapp", "-n", "default", "-f", "--tail=100"},
		},
		{
			name:         "no follow last 100 lines",
			cfg:          KubectlConfig{Resource: "deployment/myapp", Namespace: "default"},
			follow:       false,
			linesFromEnd: 100,
			wantArgs:     []string{"logs", "deployment/myapp", "-n", "default", "--tail=100"},
		},
		{
			name:         "follow from beginning",
			cfg:          KubectlConfig{Resource: "deployment/myapp", Namespace: "default"},
			follow:       true,
			linesFromEnd: -1,
			wantArgs:     []string{"logs", "deployment/myapp", "-n", "default", "-f"},
		},
		{
			name:         "no follow from beginning",
			cfg:          KubectlConfig{Resource: "deployment/myapp", Namespace: "default"},
			follow:       false,
			linesFromEnd: -1,
			wantArgs:     []string{"logs", "deployment/myapp", "-n", "default"},
		},
		{
			name:         "with container",
			cfg:          KubectlConfig{Resource: "deployment/myapp", Namespace: "flux-system", Container: "tracer"},
			follow:       true,
			linesFromEnd: 100,
			wantArgs:     []string{"logs", "deployment/myapp", "-n", "flux-system", "-c", "tracer", "-f", "--tail=100"},
		},
		{
			name:         "with label selector",
			cfg:          KubectlConfig{Resource: "-l app=tracer", Namespace: "default"},
			follow:       true,
			linesFromEnd: 50,
			wantArgs:     []string{"logs", "-l", "app=tracer", "-n", "default", "-f", "--tail=50"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := NewKubectl(tt.cfg)
			src.linesFromEnd = tt.linesFromEnd
			got := src.buildFlags(tt.follow)
			if len(got) != len(tt.wantArgs) {
				t.Fatalf("buildFlags() = %v (len=%d), want %v (len=%d)", got, len(got), tt.wantArgs, len(tt.wantArgs))
			}
			for i := range got {
				if got[i] != tt.wantArgs[i] {
					t.Fatalf("buildFlags() = %v, want %v\ndiff at index %d: got %q, want %q", got, tt.wantArgs, i, got[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestKubectlSourceBuildFlagsWithKubeconfig(t *testing.T) {
	t.Run("default namespace", func(t *testing.T) {
		src := NewKubectl(KubectlConfig{
			Resource:   "deployment/myapp",
			Namespace:  "default",
			Kubeconfig: "/etc/rancher/k3s/k3s.yaml",
		})
		src.linesFromEnd = 100
		got := src.buildFlags(true)
		want := []string{"logs", "deployment/myapp", "-n", "default", "-f", "--tail=100"}
		if len(got) != len(want) {
			t.Fatalf("buildFlags() = %v (len=%d), want %v (len=%d)", got, len(got), want, len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("buildFlags() = %v, want %v\ndiff at index %d: got %q, want %q", got, want, i, got[i], want[i])
			}
		}
		// Kubeconfig is set via env, not flags
	})
}

func TestKubectlSourceNextWithoutOpen(t *testing.T) {
	src := NewKubectl(KubectlConfig{Resource: "deployment/myapp", Namespace: "default"})
	_, err := src.Next(context.Background())
	if err == nil {
		t.Fatal("expected error when calling Next without Open")
	}
	if err.Error() != "kubectl source not open" {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Factory test – source.New() routes to KubectlSource
// ---------------------------------------------------------------------------

func TestSourceNewKubectl(t *testing.T) {
	src, err := New(Spec{
		Type: "kubectl",
		Params: map[string]string{
			"resource":  "deployment/myapp",
			"namespace": "default",
		},
	})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	if _, ok := src.(*kubectlSource); !ok {
		t.Fatalf("expected *kubectlSource, got %T", src)
	}
}

func TestSourceNewKubectlUnknownType(t *testing.T) {
	_, err := New(Spec{Type: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for unknown source type")
	}
}

// ---------------------------------------------------------------------------
// Helpers for fake kubectl process tests
// ---------------------------------------------------------------------------

// writeFakeScript writes a shell script to a temp dir and prepends that dir
// to PATH so that "kubectl" resolves to the script.
func writeFakeScript(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	old := os.Getenv("PATH")
	t.Setenv("PATH", dir+":"+old)
}

// ---------------------------------------------------------------------------
// Layer 2 – Streaming lifecycle (no follow)
// ---------------------------------------------------------------------------

func TestKubectlSourceStreaming(t *testing.T) {
	writeFakeScript(t, `
echo '{"level":"info","msg":"boot","ts":"2025-03-30T10:00:00Z"}'
echo '{"level":"info","msg":"ready","ts":"2025-03-30T10:00:01Z"}'
`)

	src := NewKubectl(KubectlConfig{Resource: "pod/test", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	line1, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if !strings.Contains(line1, "boot") {
		t.Fatalf("first line missing 'boot': %s", line1)
	}

	line2, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if !strings.Contains(line2, "ready") {
		t.Fatalf("second line missing 'ready': %s", line2)
	}

	_, err = src.Next(t.Context())
	if err != io.EOF {
		t.Fatalf("expected EOF after lines exhausted, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Layer 2 – Follow mode (fake kubectl stays alive, we push lines via a FIFO)
// ---------------------------------------------------------------------------

func TestKubectlSourceFollow(t *testing.T) {
	// Create a FIFO that the fake kubectl will read from.
	fifoDir := t.TempDir()
	fifoPath := filepath.Join(fifoDir, "kubectl.fifo")
	if err := exec.Command("mkfifo", fifoPath).Run(); err != nil {
		t.Skipf("mkfifo not available (%v), skipping follow test", err)
	}

	writeFakeScript(t, fmt.Sprintf(`
# Initial output
echo '{"level":"info","msg":"started"}'
# Then tail the FIFO forever
exec cat %s
`, fifoPath))

	src := NewKubectl(KubectlConfig{Resource: "pod/follow-test", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	// Read the initial line
	line1, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if !strings.Contains(line1, "started") {
		t.Fatalf("expected 'started', got: %s", line1)
	}

	// Now push two lines through the FIFO (in a goroutine because open on FIFO blocks)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		f, openErr := os.OpenFile(fifoPath, os.O_WRONLY, 0)
		if openErr != nil {
			t.Errorf("open fifo for writing: %v", openErr)
			return
		}
		defer f.Close()
		_, _ = fmt.Fprintln(f, `{"level":"info","msg":"pushed-1"}`)
		_, _ = fmt.Fprintln(f, `{"level":"info","msg":"pushed-2"}`)
	}()

	line2, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("second Next: %v", err)
	}
	if !strings.Contains(line2, "pushed-1") {
		t.Fatalf("expected 'pushed-1', got: %s", line2)
	}

	line3, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("third Next: %v", err)
	}
	if !strings.Contains(line3, "pushed-2") {
		t.Fatalf("expected 'pushed-2', got: %s", line3)
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Layer 3 – Seek then follow transition
//
// The source is used in two phases by consumers:
//  1. Seek to get N history lines → Next reads them → EOF
//  2. Seek(0, SeekEnd) → Next follows new lines
// ---------------------------------------------------------------------------

func TestKubectlSourceSeekThenFollow(t *testing.T) {
	fifoDir := t.TempDir()
	fifoPath := filepath.Join(fifoDir, "kubectl.fifo")
	if err := exec.Command("mkfifo", fifoPath).Run(); err != nil {
		t.Skipf("mkfifo not available (%v), skipping seek+follow test", err)
	}

	// Phase 1 (no FOLLOW_PHASE): non-follow invocation prints history.
	// Phase 2 (FOLLOW_PHASE set): follow invocation tails the FIFO.
	writeFakeScript(t, fmt.Sprintf(`
if [ -z "${FOLLOW_PHASE:-}" ]; then
	echo 'history-line-1'
	echo 'history-line-2'
else
	echo '{"level":"info","msg":"follow-started"}'
	exec cat %s
fi
`, fifoPath))

	src := NewKubectl(KubectlConfig{Resource: "pod/seek-follow", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	// ---- Phase 1: seek back and read history ----
	if _, err := src.Seek(-2, io.SeekEnd); err != nil {
		t.Fatalf("Seek(-2, SeekEnd): %v", err)
	}

	line1, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("first Next (history): %v", err)
	}
	if !strings.Contains(line1, "history-line-1") {
		t.Fatalf("expected 'history-line-1', got: %s", line1)
	}

	line2, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("second Next (history): %v", err)
	}
	if !strings.Contains(line2, "history-line-2") {
		t.Fatalf("expected 'history-line-2', got: %s", line2)
	}

	// History exhausted → EOF
	_, err = src.Next(t.Context())
	if err != io.EOF {
		t.Fatalf("expected EOF after history, got: %v", err)
	}

	// ---- Phase 2: seek to end and follow ----
	if _, err := src.Seek(0, io.SeekEnd); err != nil {
		t.Fatalf("Seek(0, SeekEnd): %v", err)
	}
	t.Setenv("FOLLOW_PHASE", "1")

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(200 * time.Millisecond)
		f, openErr := os.OpenFile(fifoPath, os.O_WRONLY, 0)
		if openErr != nil {
			t.Errorf("open fifo: %v", openErr)
			return
		}
		defer f.Close()
		_, _ = fmt.Fprintln(f, `{"level":"info","msg":"follow-line"}`)
	}()

	line3, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("follow phase Next: %v", err)
	}
	if !strings.Contains(line3, "follow-started") && !strings.Contains(line3, "follow-line") {
		t.Fatalf("expected 'follow-started' or 'follow-line', got: %s", line3)
	}

	line4, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("second follow phase Next: %v", err)
	}
	if !strings.Contains(line4, "follow-line") {
		t.Fatalf("expected 'follow-line', got: %s", line4)
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Layer 4 – Context cancellation reaps the process
// ---------------------------------------------------------------------------

func TestKubectlSourceContextCancel(t *testing.T) {
	writeFakeScript(t, `
# Block forever (exec so the process is directly the blocking command,
# ensuring the pipe closes when this process is killed).
exec sleep infinity
`)

	ctx, cancel := context.WithCancel(t.Context())
	src := NewKubectl(KubectlConfig{Resource: "pod/cancel-test", Namespace: "default"})
	if err := src.Open(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	// Start a goroutine that tries to read (will block because fake kubectl
	// just sleeps).  We'll cancel the context and verify we get a context error.
	errCh := make(chan error, 1)
	go func() {
		_, err := src.Next(ctx)
		errCh <- err
	}()

	// Give the process time to start
	time.Sleep(100 * time.Millisecond)

	// Cancel the context
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error after context cancel, got nil")
		}
		// The command is killed by exec.CommandContext, which closes the
		// stdout pipe and causes scanner.Scan() to return false (EOF).
		// Alternatively, if the ctx.Err() check fires first we get a
		// context cancellation error.  Accept both.
		if !strings.Contains(err.Error(), "context canceled") &&
			err.Error() != "context canceled" &&
			err != io.EOF {
			t.Fatalf("expected context cancel or EOF, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Next to return after cancel")
	}

	// Close should succeed and clean up the killed process
	if err := src.Close(); err != nil {
		t.Fatalf("Close after cancel: %v", err)
	}
	if src.cmd != nil {
		t.Fatal("cmd should be nil after Close")
	}
	if src.scanner != nil {
		t.Fatal("scanner should be nil after Close")
	}
}

// ---------------------------------------------------------------------------
// Layer 4 – Close while blocked in follow reaps the process
// ---------------------------------------------------------------------------

func TestKubectlSourceCloseDuringFollow(t *testing.T) {
	writeFakeScript(t, `
echo 'started'
exec sleep infinity
`)

	src := NewKubectl(KubectlConfig{Resource: "pod/close-test", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Read the first line
	line1, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("first Next: %v", err)
	}
	if !strings.Contains(line1, "started") {
		t.Fatalf("expected 'started', got: %s", line1)
	}

	// Close should kill the process even though it's blocked
	if err := src.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// After close, cmd should be nil
	if src.cmd != nil {
		t.Fatal("cmd should be nil after Close")
	}
	if src.scanner != nil {
		t.Fatal("scanner should be nil after Close")
	}
}
