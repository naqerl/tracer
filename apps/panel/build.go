// Package panel provides build-time metadata for the tracer panel binary.
// The Version variable is injected at build time via ldflags.
package panel

// Version is the tracer release tag used for this build (set via ldflags,
// e.g. -X tracer/apps/panel.Version=v0.1.31).
var Version = "unknown"
