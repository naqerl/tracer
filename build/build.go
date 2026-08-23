// Package build provides build-time configuration and utilities.
// All build-time ldflags variables are injected into this package.
package build

// Commit is the git commit hash at build time (set via ldflags).
var Commit = "unknown"

// Ref is the git ref used for the build (e.g., "main", tag name) - set via ldflags.
var Ref = "unknown"

// TracerGroup is the system group used for tracer services (set via ldflags).
var TracerGroup = "tracer"

// Bindir is the directory where binaries are installed (set via ldflags).
var Bindir = "/usr/local/bin"

// WithSystemd controls systemd integration: "yes", "no", or "auto" (detect at runtime).
var WithSystemd = "auto"

// PanelUser is the system user for the panel service (set via ldflags).
var PanelUser = "tracer-panel"

// CollectorUser is the system user for the collector service (set via ldflags).
var CollectorUser = "tracer-collector"

// CollectorName is the collector's own systemd unit name.
var CollectorName = "tracer-collector"
