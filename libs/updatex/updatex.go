// Package updatex provides self-update functionality for tracer binaries.
// Updates use binary download from a URL, checksum verification, and atomic
// rename to swap the running binary. The package supports a two-phase flow:
//
//  1. Prepare (download + stage): download a new binary, verify its checksum,
//     run a pre-flight check (--version), and store it alongside the running binary.
//  2. Activate (atomic rename): swap the staged binary into place atomically.
//
// This replaces the old source-based rebuild approach.
package updatex

import (
	"context"
	"fmt"
	"time"
)

// UpdateBinary downloads a binary from url, verifies its sha256 checksum, and
// stages it alongside the running binary for later activation. Returns the path
// to the staged binary.
//
// After a successful download the caller should persist the version+checksum
// in a manifest so that ActivateStaged can verify integrity at activation time.
func UpdateBinary(ctx context.Context, env Environment, url, checksum string) (stagedPath string, err error) {
	orch := NewOrchestrator(env)

	if err := orch.Download(ctx, url); err != nil {
		return "", fmt.Errorf("could not download update: %w", err)
	}

	if checksum != "" {
		if err := orch.Verify(checksum); err != nil {
			return "", fmt.Errorf("could not verify update: %w", err)
		}
	}

	if _, err := orch.PreFlight(ctx); err != nil {
		return "", fmt.Errorf("could not verify staged binary: %w", err)
	}

	return orch.StagedPath(), nil
}

// ActivateStaged performs an atomic rename of the staged binary to the final
// binary path. It runs a post-flight check on the new binary before removing
// the old one.
func ActivateStaged(ctx context.Context, env Environment) error {
	orch := NewOrchestrator(env)

	manifest, err := orch.Activate(ctx)
	if err != nil {
		return fmt.Errorf("could not activate update: %w", err)
	}

	// Persist the manifest so that future reconnections can report the version.
	if err := WriteManifest(env.BinaryDir(), manifest); err != nil {
		return fmt.Errorf("could not write manifest: %w", err)
	}

	return nil
}

// StagedVersion runs the staged binary's --version and returns the output.
// Returns ("", false) if no staged binary exists or if it fails.
func StagedVersion(env Environment) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	orch := NewOrchestrator(env)
	staged := orch.StagedPath()
	if staged == "" {
		return "", false
	}
	version, err := env.VersionOf(ctx, staged)
	if err != nil {
		return "", false
	}
	return version, true
}
