package updatex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	// stagedName is the filename used for the downloaded binary before activation.
	stagedName = ".tracer-update.staged"
	// backupName is the filename for the old binary during activation.
	backupName = ".tracer-update.bak"
)

// Orchestrator handles the low-level operations of downloading, verifying, and
// atomically replacing a binary. It is the core engine behind UpdateBinary and
// ActivateStaged.
type Orchestrator struct {
	env       Environment
	stagedDir string
}

// NewOrchestrator creates an Orchestrator for the given environment.
func NewOrchestrator(env Environment) *Orchestrator {
	return &Orchestrator{
		env:       env,
		stagedDir: env.BinaryDir(),
	}
}

// Download fetches the binary from url and writes it to the staged path.
// If the staged path already exists it is overwritten.
func (o *Orchestrator) Download(ctx context.Context, url string) error {
	staged := o.StagedPath()
	slog.Info("downloading binary", "url", url, "dest", staged)

	tmpPath := staged + ".downloading"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("could not create temp file %s: %w", tmpPath, err)
	}

	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if reqErr != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("could not create request: %w", reqErr)
	}

	resp, doErr := http.DefaultClient.Do(req)
	if doErr != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("could not download %s: %w", url, doErr)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("download %s: unexpected status %d", url, resp.StatusCode)
	}

	written, copyErr := io.Copy(f, resp.Body)
	if copyErr != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("could not download %s: %w", url, copyErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("could not close temp file: %w", closeErr)
	}

	slog.Info("downloaded binary", "size", written, "dest", staged)

	if err := os.Rename(tmpPath, staged); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("could not rename temp file to %s: %w", staged, err)
	}

	if err := os.Chmod(staged, 0755); err != nil {
		return fmt.Errorf("could not chmod staged binary: %w", err)
	}

	return nil
}

// StagedPath returns the path where the downloaded binary is stored.
func (o *Orchestrator) StagedPath() string {
	return filepath.Join(o.stagedDir, stagedName)
}

// backupPath returns where the current binary is backed up during activation.
func (o *Orchestrator) backupPath() string {
	return filepath.Join(o.stagedDir, backupName)
}

// Verify checks that the staged binary matches the given sha256 hex checksum.
// If checksum is empty the check is skipped.
func (o *Orchestrator) Verify(expectedHex string) error {
	if expectedHex == "" {
		return nil
	}

	staged := o.StagedPath()
	f, err := os.Open(staged)
	if err != nil {
		return fmt.Errorf("could not open staged binary for checksum: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("could not hash staged binary: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))

	if got != expectedHex {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHex, got)
	}
	return nil
}

// PreFlight runs the staged binary with --version to verify it is executable.
func (o *Orchestrator) PreFlight(ctx context.Context) (string, error) {
	staged := o.StagedPath()
	if _, err := os.Stat(staged); err != nil {
		return "", fmt.Errorf("pre-flight check failed: staged binary not found at %s: %w", staged, err)
	}
	version, err := o.env.VersionOf(ctx, staged)
	if err != nil {
		return "", fmt.Errorf("pre-flight check failed: %w", err)
	}
	slog.Info("staged binary version", "version", version)
	return version, nil
}

// Activate performs an atomic rename of the staged binary to the final path.
// It backs up the current binary first and runs a post-flight check.
func (o *Orchestrator) Activate(ctx context.Context) (*Manifest, error) {
	binaryPath := o.env.BinaryPath()
	staged := o.StagedPath()
	backup := o.backupPath()

	// Verify the staged binary exists.
	if _, err := os.Stat(staged); err != nil {
		return nil, fmt.Errorf("no staged binary at %s: %w", staged, err)
	}

	// Back up the current binary.
	slog.Info("backing up current binary", "src", binaryPath, "dst", backup)
	if err := copyFile(binaryPath, backup); err != nil {
		return nil, fmt.Errorf("could not back up current binary: %w", err)
	}

	// Atomic rename: staged → binary.
	slog.Info("activating staged binary", "src", staged, "dst", binaryPath)
	if err := os.Rename(staged, binaryPath); err != nil {
		// Attempt to restore from backup.
		if rbErr := os.Rename(backup, binaryPath); rbErr != nil {
			slog.Error("could not restore backup after failed activate", "error", rbErr)
		}
		return nil, fmt.Errorf("could not rename staged to binary: %w", err)
	}

	if err := os.Chmod(binaryPath, 0755); err != nil {
		slog.Warn("could not chmod activated binary", "error", err)
	}

	// Post-flight: verify the new binary works.
	version, err := o.env.VersionOf(ctx, binaryPath)
	if err != nil {
		// New binary is broken — restore from backup.
		slog.Error("post-flight check failed, restoring backup", "error", err)
		if rbErr := os.Rename(backup, binaryPath); rbErr != nil {
			slog.Error("could not restore backup after failed post-flight", "error", rbErr)
		}
		return nil, fmt.Errorf("post-flight check failed on new binary: %w", err)
	}

	slog.Info("update activated successfully", "version", version)

	// Remove backup.
	if err := os.Remove(backup); err != nil {
		slog.Warn("could not remove backup", "path", backup, "error", err)
	}

	return &Manifest{
		BinaryPath:  binaryPath,
		Version:     version,
		ActivatedAt: time.Now(),
	}, nil
}

// copyFile copies src to dst, preserving permissions.
func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()

	d, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(d, s); err != nil {
		d.Close()
		os.Remove(dst)
		return err
	}

	if err := d.Close(); err != nil {
		os.Remove(dst)
		return err
	}

	// Preserve permissions.
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode())
}
