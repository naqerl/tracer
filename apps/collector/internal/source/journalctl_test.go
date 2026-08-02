package source

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeJournalctl writes a shell script to a temp dir and prepends that
// dir to PATH so that "journalctl" resolves to the script.
func writeFakeJournalctl(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "journalctl")
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0755); err != nil {
		t.Fatalf("write fake journalctl: %v", err)
	}
	old := os.Getenv("PATH")
	t.Setenv("PATH", dir+":"+old)
}

func TestJournalctlSourceStreaming(t *testing.T) {
	writeFakeJournalctl(t, `
echo 'time=2025-03-30T10:00:00Z level=INFO msg="boot"'
echo 'time=2025-03-30T10:00:01Z level=INFO msg="ready"'
`)

	src := NewJournalctl(JournalctlConfig{Unit: "demo.service"})
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

	// Clean exit (status 0) is reported as EOF, not an error.
	if _, err := src.Next(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after lines exhausted, got %v", err)
	}
	// A repeated Next after EOF must stay EOF (no double Wait).
	if _, err := src.Next(t.Context()); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF on repeated Next, got %v", err)
	}
}

func TestJournalctlSourceNonZeroExitReportsStderr(t *testing.T) {
	writeFakeJournalctl(t, `
echo 'Failed to get journal fields: unit demo.service not found' >&2
exit 1
`)

	src := NewJournalctl(JournalctlConfig{Unit: "demo.service"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	_, err := src.Next(t.Context())
	if err == nil {
		t.Fatal("expected an error for a non-zero journalctl exit")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("expected a real error, got bare EOF: %v", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error should include journalctl stderr, got: %v", err)
	}
}
