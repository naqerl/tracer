package updatex

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testEnv struct {
	binaryPath string
	binaryDir  string
	version    string
}

func (e *testEnv) BinaryPath() string                                    { return e.binaryPath }
func (e *testEnv) BinaryDir() string                                     { return e.binaryDir }
func (e *testEnv) VersionOf(_ context.Context, _ string) (string, error) { return e.version, nil }

func TestCleanupStaged(t *testing.T) {
	dir := t.TempDir()
	env := &testEnv{binaryDir: dir, binaryPath: filepath.Join(dir, "binary")}

	// Create old staged file.
	oldStaged := filepath.Join(dir, stagedName)
	if err := os.WriteFile(oldStaged, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	// Set mtime to 2 hours ago.
	twoHoursAgo := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(oldStaged, twoHoursAgo, twoHoursAgo); err != nil {
		t.Fatal(err)
	}

	// Create recent downloading file.
	recentDL := filepath.Join(dir, stagedName+".downloading")
	if err := os.WriteFile(recentDL, []byte("recent"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := CleanupStaged(env, 1*time.Hour); err != nil {
		t.Fatalf("CleanupStaged: %v", err)
	}

	// Old staged file should be removed.
	if _, err := os.Stat(oldStaged); !os.IsNotExist(err) {
		t.Error("old staged file was not cleaned up")
	}

	// Recent downloading file should remain.
	if _, err := os.Stat(recentDL); err != nil {
		t.Error("recent downloading file was incorrectly cleaned up")
	}
}

func TestCleanupAll(t *testing.T) {
	dir := t.TempDir()
	env := &testEnv{binaryDir: dir, binaryPath: filepath.Join(dir, "binary")}

	// Create staged and backup files.
	for _, name := range []string{stagedName, backupName} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	if err := CleanupAll(env); err != nil {
		t.Fatalf("CleanupAll: %v", err)
	}

	for _, name := range []string{stagedName, backupName} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("file %s was not cleaned up", name)
		}
	}
}
