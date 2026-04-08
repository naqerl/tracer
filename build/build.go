// Package build provides build-time configuration and utilities.
package build

import (
	_ "embed"
)

//go:embed config.mk
var ConfigMK string

// Commit is the git commit hash at build time (set via ldflags).
var Commit = "unknown"

// Ref is the git ref used for the build (e.g., "main", tag name) - set via ldflags.
var Ref = "unknown"
