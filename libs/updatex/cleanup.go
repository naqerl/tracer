package updatex

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// CleanupStaged removes staged and backup files that are older than the given
// duration. This should be called periodically to prevent the binary directory
// from accumulating stale update artifacts.
func CleanupStaged(env Environment, keepNewerThan time.Duration) error {
	dir := env.BinaryDir()
	cutoff := time.Now().Add(-keepNewerThan)
	var lastErr error

	patterns := []string{stagedName, stagedName + ".downloading", backupName}
	for _, name := range patterns {
		path := filepath.Join(dir, name)
		if err := cleanupFileIfOlder(path, cutoff); err != nil {
			slog.Warn("could not clean up staged file", "path", path, "error", err)
			lastErr = err
		}
	}

	return lastErr
}

// CleanupAll removes all staged and backup files regardless of age.
func CleanupAll(env Environment) error {
	dir := env.BinaryDir()
	var lastErr error

	patterns := []string{stagedName, stagedName + ".downloading", backupName}
	for _, name := range patterns {
		path := filepath.Join(dir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("could not remove file", "path", path, "error", err)
			lastErr = err
		}
	}

	return lastErr
}

// cleanupFileIfOlder removes path if it exists and its mtime is before cutoff.
func cleanupFileIfOlder(path string, cutoff time.Time) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.ModTime().Before(cutoff) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
