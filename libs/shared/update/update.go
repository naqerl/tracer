// Package update provides self-update functionality for tracer binaries.
// Note: Self-updates work by rebuilding from source. The main branch always
// contains the latest stable version.
package update

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	goRuntime "runtime"
	"strings"
	"time"

	"tracer/build"
	"tracer/libs/shared/runtime"
)

const (
	repoURL   = "https://git.naqerl.com/eco/tracer"
	stableRef = "main" // main branch contains latest stable release
)

// BinaryType represents the type of tracer binary.
type BinaryType string

const (
	BinaryCollector BinaryType = "tracer-collector"
	BinaryPanel     BinaryType = "tracer-panel"
)

// SelfUpdate performs a self-update for the given binary type.
func SelfUpdate(binary BinaryType) error {
	return Update(Options{
		BinaryName: binary,
		VerifyFn:   VerifyBinary,
		PostUpdate: func() error {
			return runtime.NewManager(string(binary)).Restart()
		},
	})
}

// VerifyBinary verifies a newly built binary by running it with --version.
func VerifyBinary(path string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("binary verification failed (exit: %w, output: %s)", err, string(output))
	}
	if len(output) == 0 {
		return fmt.Errorf("binary produced no output for --version")
	}
	slog.Info("Binary verified", "path", path, "output", strings.TrimSpace(string(output)))
	return nil
}

// Options configures the update process.
type Options struct {
	BinaryName BinaryType

	// PostUpdate is called after the binary has been successfully installed.
	// It should trigger a restart or notify the user. If nil, a default
	// message is printed.
	PostUpdate func() error

	// VerifyFn is called with the path to the newly built binary before it
	// replaces the running binary. If it returns an error, the update is
	// aborted and the old binary is restored.
	VerifyFn func(binaryPath string) error
}

// Update performs a self-update by rebuilding from source.
func Update(opts Options) error {
	// Check if we have the source available
	srcDir := "/tmp/tracer-update"

	fmt.Println("Updating tracer from source...")
	fmt.Printf("Repository: %s\n", repoURL)

	// Clean up previous attempts
	os.RemoveAll(srcDir)

	// Download source from cgit snapshot
	fmt.Println("Downloading source snapshot...")
	if err := downloadSnapshot(srcDir); err != nil {
		return fmt.Errorf("download source: %w", err)
	}

	// Save and change to source directory
	origDir, err := os.Getwd()
	if err != nil {
		os.RemoveAll(srcDir)
		return fmt.Errorf("get current dir: %w", err)
	}
	if err := os.Chdir(srcDir); err != nil {
		os.RemoveAll(srcDir)
		return fmt.Errorf("change to source dir: %w", err)
	}
	defer os.Chdir(origDir) // restore working directory on return

	// Build the binary
	fmt.Println("Building from source...")

	var buildTarget string
	switch opts.BinaryName {
	case BinaryCollector:
		buildTarget = "build-collector"
	case BinaryPanel:
		buildTarget = "build-panel"
	default:
		return fmt.Errorf("unknown binary type: %s", opts.BinaryName)
	}

	cmd := exec.Command("make", buildTarget)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	// Locate source and destination paths
	binaryPath := filepath.Join(srcDir, "bin", string(opts.BinaryName))
	mgr := runtime.NewManager(string(opts.BinaryName))
	destPath := filepath.Join(mgr.BinaryDir(), string(opts.BinaryName))

	// Verify the built binary before installing
	if opts.VerifyFn != nil {
		fmt.Println("Verifying binary...")
		if err := opts.VerifyFn(binaryPath); err != nil {
			os.RemoveAll(srcDir)
			return fmt.Errorf("binary verification failed: %w", err)
		}
	}

	// Backup existing binary if present
	backupPath := destPath + ".bak"
	if _, err := os.Stat(destPath); err == nil {
		fmt.Println("Backing up existing binary...")
		if err := copyFile(destPath, backupPath); err != nil {
			os.RemoveAll(srcDir)
			return fmt.Errorf("backup failed: %w", err)
		}
		// Schedule backup removal on success
		defer func() {
			if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
				fmt.Printf("Warning: could not remove backup: %v\n", err)
			}
		}()
	}

	// Install the new binary
	fmt.Printf("Installing to %s...\n", destPath)
	if err := copyFile(binaryPath, destPath); err != nil {
		// Restore backup
		if _, e := os.Stat(backupPath); e == nil {
			fmt.Println("Restoring previous binary from backup...")
			if restoreErr := os.Rename(backupPath, destPath); restoreErr != nil {
				fmt.Printf("CRITICAL: failed to restore backup: %v\n", restoreErr)
			}
		}
		os.RemoveAll(srcDir)
		return fmt.Errorf("install failed: %w", err)
	}

	// Make executable (copyFile preserves mode, but ensure it's executable)
	os.Chmod(destPath, 0755)

	// Cleanup temp source
	os.RemoveAll(srcDir)

	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("Update successful!")
	fmt.Println("========================================")
	fmt.Println()

	// Run post-update callback (e.g., restart)
	if opts.PostUpdate != nil {
		fmt.Println("Triggering post-update action...")
		if err := opts.PostUpdate(); err != nil {
			// Log but don't fail — binary is already installed
			fmt.Printf("Warning: post-update action failed: %v\n", err)
		}
	}

	return nil
}

// downloadSnapshot downloads source tarball from cgit (main branch - stable)
func downloadSnapshot(destDir string) error {
	snapshotURL := repoURL + "/snapshot/tracer-" + stableRef + ".tar.gz"

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	cmd := exec.Command("curl", "-fsSL", snapshotURL)
	cmd2 := exec.Command("tar", "-xz", "-C", destDir, "--strip-components=1")

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd2.Stdin = pipe
	cmd2.Stdout = os.Stdout
	cmd2.Stderr = os.Stderr

	if err := cmd2.Start(); err != nil {
		return err
	}
	if err := cmd.Run(); err != nil {
		return err
	}
	if err := cmd2.Wait(); err != nil {
		return err
	}

	return nil
}

// CheckUpdate checks if an update is available by comparing the local
// embedded commit hash with the remote commit hash.
func CheckUpdate() (bool, string, error) {
	// Get remote commit hash from the stable ref
	remoteCommit, err := getRemoteCommit()
	if err != nil {
		return false, "", fmt.Errorf("check remote version: %w", err)
	}

	localCommit := build.Commit
	if localCommit == "unknown" {
		// Cannot determine local version - assume update available
		return true, fmt.Sprintf("unknown local version, remote is %s", remoteCommit[:7]), nil
	}

	// Compare first 7 chars (short hash) since local uses git rev-parse --short
	localShort := localCommit
	if len(localCommit) > 7 {
		localShort = localCommit[:7]
	}
	remoteShort := remoteCommit
	if len(remoteCommit) > 7 {
		remoteShort = remoteCommit[:7]
	}

	if localShort == remoteShort {
		return false, fmt.Sprintf("up to date (%s)", localShort), nil
	}

	return true, fmt.Sprintf("%s → %s", localShort, remoteShort), nil
}

// getRemoteCommit fetches the commit hash of the stable ref from the remote.
func getRemoteCommit() (string, error) {
	// Use git ls-remote to get the commit hash without cloning
	// Timeout to avoid hanging if network is unavailable
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "ls-remote", repoURL, "refs/heads/"+stableRef)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git ls-remote failed: %w", err)
	}

	// Parse output: "<commit-hash>\t<ref>"
	fields := strings.Fields(string(output))
	if len(fields) < 1 {
		return "", fmt.Errorf("unexpected output from git ls-remote")
	}

	return fields[0], nil
}

// DetectArchitecture returns the system architecture.
func DetectArchitecture() string {
	arch := goRuntime.GOARCH
	switch arch {
	case "amd64", "arm64":
		return arch
	default:
		return arch
	}
}

// copyFile copies a file from src to dst, preserving permissions.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Create parent directory if needed
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}

	// Preserve source file permissions
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	return os.Chmod(dst, fi.Mode())
}
