//go:build linux

package source

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileSourcePartialLineAndAppend(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	src := NewFile(FileConfig{Path: path, ReadFrom: "end"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatalf("open source: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	type nextResult struct {
		line string
		err  error
	}
	results := make(chan nextResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		line, err := src.Next(ctx)
		results <- nextResult{line: line, err: err}
	}()

	if err := appendToFile(path, "par"); err != nil {
		t.Fatalf("append partial: %v", err)
	}

	select {
	case got := <-results:
		t.Fatalf("next returned early: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}

	if err := appendToFile(path, "tial\n"); err != nil {
		t.Fatalf("append completion: %v", err)
	}

	select {
	case got := <-results:
		if got.err != nil {
			t.Fatalf("next error: %v", got.err)
		}
		if got.line != "partial" {
			t.Fatalf("unexpected line: %q", got.line)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for line")
	}
}

func TestFileSourceHandlesRenameAndRecreate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("boot\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	src := NewFile(FileConfig{Path: path, ReadFrom: "end"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatalf("open source: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	rotatedPath := filepath.Join(dir, "app.log.1")
	if err := os.Rename(path, rotatedPath); err != nil {
		t.Fatalf("rename file: %v", err)
	}
	if err := os.WriteFile(path, []byte("after-rotate\n"), 0o644); err != nil {
		t.Fatalf("write replacement file: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	line, err := src.Next(ctx)
	if err != nil {
		t.Fatalf("next error: %v", err)
	}
	if line != "after-rotate" {
		t.Fatalf("unexpected line: %q", line)
	}
}

func TestFileSourceHandlesTruncate(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	src := NewFile(FileConfig{Path: path, ReadFrom: "end"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatalf("open source: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	if err := appendToFile(path, "before-truncate\n"); err != nil {
		t.Fatalf("append before truncate: %v", err)
	}
	ctx1, cancel1 := context.WithTimeout(t.Context(), 2*time.Second)
	line1, err := src.Next(ctx1)
	cancel1()
	if err != nil {
		t.Fatalf("next before truncate: %v", err)
	}
	if line1 != "before-truncate" {
		t.Fatalf("unexpected line before truncate: %q", line1)
	}

	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate file: %v", err)
	}
	if err := appendToFile(path, "after-truncate\n"); err != nil {
		t.Fatalf("append after truncate: %v", err)
	}

	ctx2, cancel2 := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel2()
	line2, err := src.Next(ctx2)
	if err != nil {
		t.Fatalf("next after truncate: %v", err)
	}
	if line2 != "after-truncate" {
		t.Fatalf("unexpected line after truncate: %q", line2)
	}
}

func TestFileSourceReadFromValidation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	src := NewFile(FileConfig{Path: path, ReadFrom: "middle"})
	err := src.Open(t.Context())
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "invalid read_from") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFileSourceSeekToStart(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	// Open from end to start at EOF
	src := NewFile(FileConfig{Path: path, ReadFrom: "end"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer src.Close()

	// Seek to start
	seeker, ok := src.(io.Seeker)
	if !ok {
		t.Fatal("fileSource should implement io.Seeker")
	}

	pos, err := seeker.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("seek to start: %v", err)
	}
	if pos != 0 {
		t.Fatalf("expected position 0, got %d", pos)
	}

	// Should be able to read from beginning
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	line, err := src.Next(ctx)
	if err != nil {
		t.Fatalf("next error: %v", err)
	}
	if line != "line1" {
		t.Fatalf("expected line1, got %q", line)
	}
}

func TestFileSourceSeekToEnd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("line1\nline2\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	// Open from start
	src := NewFile(FileConfig{Path: path, ReadFrom: "start"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer src.Close()

	// Read first line
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	line, err := src.Next(ctx)
	cancel()
	if err != nil {
		t.Fatalf("next error: %v", err)
	}
	if line != "line1" {
		t.Fatalf("expected line1, got %q", line)
	}

	// Seek to end
	seeker := src.(io.Seeker)
	pos, err := seeker.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("seek to end: %v", err)
	}

	// Position should be at file size
	info, _ := os.Stat(path)
	if pos != info.Size() {
		t.Fatalf("expected position %d, got %d", info.Size(), pos)
	}

	// Next read should timeout (no new content)
	ctx2, cancel2 := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel2()
	_, err = src.Next(ctx2)
	if err != context.DeadlineExceeded {
		t.Fatalf("expected timeout at EOF, got: %v", err)
	}
}

func TestFileSourceSeekCurrent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	src := NewFile(FileConfig{Path: path, ReadFrom: "start"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer src.Close()

	seeker := src.(io.Seeker)

	// Read first line
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	line, err := src.Next(ctx)
	cancel()
	if err != nil {
		t.Fatalf("next error: %v", err)
	}
	if line != "line1" {
		t.Fatalf("expected line1, got %q", line)
	}

	// Get current position
	pos, err := seeker.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("seek current: %v", err)
	}

	// Position should be after "line1\n" (6 bytes)
	if pos != 6 {
		t.Fatalf("expected position 6, got %d", pos)
	}

	// Seek forward by 6 bytes (skip line2\n)
	newPos, err := seeker.Seek(6, io.SeekCurrent)
	if err != nil {
		t.Fatalf("seek relative: %v", err)
	}
	if newPos != 12 {
		t.Fatalf("expected position 12, got %d", newPos)
	}

	// Should read line3
	ctx2, cancel2 := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel2()
	line, err = src.Next(ctx2)
	if err != nil {
		t.Fatalf("next error: %v", err)
	}
	if line != "line3" {
		t.Fatalf("expected line3, got %q", line)
	}
}

func TestFileSourceSeekClearsPartialBuffer(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("complete\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	src := NewFile(FileConfig{Path: path, ReadFrom: "start"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer src.Close()

	seeker := src.(io.Seeker)

	// Append partial line (no newline)
	if err := appendToFile(path, "partial"); err != nil {
		t.Fatalf("append partial: %v", err)
	}

	// Read - should get "complete" and start buffering "partial"
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	line, err := src.Next(ctx)
	cancel()
	if err != nil {
		t.Fatalf("next error: %v", err)
	}
	if line != "complete" {
		t.Fatalf("expected 'complete', got %q", line)
	}

	// Seek back to start - should clear partial buffer
	_, err = seeker.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("seek to start: %v", err)
	}

	// Read first line again
	ctx2, cancel2 := context.WithTimeout(t.Context(), 2*time.Second)
	line, err = src.Next(ctx2)
	cancel2()
	if err != nil {
		t.Fatalf("next error: %v", err)
	}
	if line != "complete" {
		t.Fatalf("expected 'complete' after seek, got %q", line)
	}

	// The "partial" text should have been discarded by the seek
	// Complete the partial line
	if err := appendToFile(path, "-rest\n"); err != nil {
		t.Fatalf("append rest: %v", err)
	}

	// Wait for file event to be processed
	time.Sleep(100 * time.Millisecond)

	// Read - should get the newly completed line
	ctx3, cancel3 := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel3()
	line, err = src.Next(ctx3)
	if err != nil {
		t.Fatalf("next error: %v", err)
	}
	// Since we seeked back, we should see partial-rest as a new line
	if line != "partial-rest" {
		t.Fatalf("expected 'partial-rest', got %q", line)
	}
}

func TestFileSourceSeekWhenNotOpen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("line\n"), 0o644); err != nil {
		t.Fatalf("write initial file: %v", err)
	}

	src := NewFile(FileConfig{Path: path, ReadFrom: "start"})
	// Don't open

	seeker, ok := src.(io.Seeker)
	if !ok {
		t.Fatal("fileSource should implement io.Seeker")
	}

	_, err := seeker.Seek(0, io.SeekStart)
	if err == nil {
		t.Fatal("expected error when seeking closed source")
	}
	if !strings.Contains(err.Error(), "not open") {
		t.Fatalf("expected 'not open' error, got: %v", err)
	}
}

func appendToFile(path, s string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(s)
	return err
}
