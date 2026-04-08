// Package updater provides self-update functionality for tracer binaries.
// Note: Self-updates work by rebuilding from source. The main branch always
// contains the latest stable version.
package updater

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"tracer/build"
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

// Options configures the update process.
type Options struct {
	BinaryName BinaryType
	PostUpdate func()
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

	// Write embedded config.mk to restore original configure options
	if build.ConfigMK != "" {
		fmt.Println("Restoring original build configuration...")
		configPath := filepath.Join(srcDir, "build", "configure.mk")

		// Ensure build directory exists
		if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
			return fmt.Errorf("create build directory: %w", err)
		}

		if err := os.WriteFile(configPath, []byte(build.ConfigMK), 0644); err != nil {
			return fmt.Errorf("write configure.mk: %w", err)
		}

		// Also create symlink at root for backwards compatibility
		configMkPath := filepath.Join(srcDir, "config.mk")
		os.Remove(configMkPath) // Remove if exists
		if err := os.Symlink("build/configure.mk", configMkPath); err != nil {
			// If symlink fails, just copy the file
			os.WriteFile(configMkPath, []byte(build.ConfigMK), 0644)
		}
	}

	// Change to source directory
	os.Chdir(srcDir)

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

	// Install the binary
	fmt.Println("Installing...")
	binaryPath := filepath.Join(srcDir, "bin", string(opts.BinaryName))
	destPath := filepath.Join("/usr/local/bin", string(opts.BinaryName))

	cmd = exec.Command("cp", binaryPath, destPath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	// Cleanup
	os.RemoveAll(srcDir)

	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("Update successful!")
	fmt.Println("========================================")
	fmt.Println()

	if opts.PostUpdate != nil {
		opts.PostUpdate()
	}

	return nil
}

// downloadSnapshot downloads source tarball from cgit (main branch - stable)
func downloadSnapshot(destDir string) error {
	snapshotURL := repoURL + "/snapshot/tracer-" + stableRef + ".tar.gz"

	os.MkdirAll(destDir, 0755)

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
	arch := runtime.GOARCH
	switch arch {
	case "amd64", "arm64":
		return arch
	default:
		return arch
	}
}

// ParseConfigFlags extracts configure flags from embedded config.mk
func ParseConfigFlags() string {
	if build.ConfigMK == "" {
		return ""
	}

	var flags []string
	lines := strings.Split(build.ConfigMK, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Skip comments and empty lines
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse key = value pairs
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Map config.mk variables to configure flags
		switch key {
		case "PREFIX":
			flags = append(flags, fmt.Sprintf("--prefix=%s", value))
		case "BINDIR":
			flags = append(flags, fmt.Sprintf("--bindir=%s", value))
		case "SYSCONFDIR":
			flags = append(flags, fmt.Sprintf("--sysconfdir=%s", value))
		case "LOCALSTATEDIR":
			flags = append(flags, fmt.Sprintf("--localstatedir=%s", value))
		case "WITH_SYSTEMD":
			if value == "no" {
				flags = append(flags, "--without-systemd")
			}
		case "WITH_PANEL":
			if value == "no" {
				flags = append(flags, "--without-panel")
			}
		}
	}

	return strings.Join(flags, " ")
}
