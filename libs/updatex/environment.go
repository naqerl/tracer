package updatex

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Environment provides filesystem paths and version detection for the running
// binary. The interface allows testing without actual binaries.
type Environment interface {
	// BinaryPath returns the absolute path of the running binary (the file to
	// be replaced on activation).
	BinaryPath() string

	// BinaryDir returns the directory containing the running binary. This is
	// where staged binaries and manifests are stored.
	BinaryDir() string

	// VersionOf runs the binary at path with --version and returns the trimmed
	// output. Used for pre-flight and post-flight validation.
	VersionOf(ctx context.Context, path string) (string, error)
}

// ProdEnvironment implements Environment for the production system. It uses
// os.Executable and exec.Command for version detection.
type ProdEnvironment struct{}

func (ProdEnvironment) BinaryPath() string {
	path, err := os.Executable()
	if err != nil {
		// Fallback: should never happen in production, but provides a sensible
		// default for development.
		return "/usr/local/bin/tracer-collector"
	}
	return path
}

func (e ProdEnvironment) BinaryDir() string {
	return filepath.Dir(e.BinaryPath())
}

func (ProdEnvironment) VersionOf(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, path, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("could not get version of %s: %w", path, err)
	}
	return strings.TrimSpace(string(out)), nil
}
