// Package update provides self-update functionality for tracer binaries.
// Note: Self-updates work by rebuilding from source. The main branch always
// contains the latest stable version.
package updatex

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
	"tracer/libs/shared/runtimex"
)

const (
	repoURL   = "https://git.naqerl.com/eco/tracer"
	stableRef = "main" // main branch contains latest stable release
)

// SelfUpdate performs a self-update for the given service slug.
// It returns a Report whose Events channel emits progress at each stage.
// Call Wait() on the Report to block until the update completes.
func SelfUpdate(slug runtimex.Slug) *Report {
	return NewUpdater(slug).Run()
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
