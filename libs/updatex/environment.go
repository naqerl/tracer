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
//
// BinaryPathOverride, if non-empty, is returned by BinaryPath instead of
// os.Executable. Set this in tests or when os.Executable is unreliable.
// ProdEnvironment with an empty BinaryPathOverride works correctly for both
// collector and panel — it probes os.Executable and falls back to "" if that
// fails (callers receive a sensible error downstream).
type ProdEnvironment struct {
	BinaryPathOverride string
}

func (e ProdEnvironment) BinaryPath() string {
	if e.BinaryPathOverride != "" {
		return e.BinaryPathOverride
	}
	path, err := os.Executable()
	if err != nil {
		return ""
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
