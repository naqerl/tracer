//go:build linux

package source

import (
	"context"
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
	if err := src.Open(context.Background()); err != nil {
		t.Fatalf("open source: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	type nextResult struct {
		line string
		err  error
	}
	results := make(chan nextResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
	if err := src.Open(context.Background()); err != nil {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
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
	if err := src.Open(context.Background()); err != nil {
		t.Fatalf("open source: %v", err)
	}
	t.Cleanup(func() { _ = src.Close() })

	if err := appendToFile(path, "before-truncate\n"); err != nil {
		t.Fatalf("append before truncate: %v", err)
	}
	ctx1, cancel1 := context.WithTimeout(context.Background(), 2*time.Second)
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

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
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
	err := src.Open(context.Background())
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "invalid read_from") {
		t.Fatalf("unexpected error: %v", err)
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
