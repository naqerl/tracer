// Package collector provides build-time metadata for the tracer collector binary.
// The Version variable is injected at build time via ldflags.
package collector

// Version is the tracer release tag used for this build (set via ldflags,
// e.g. -X tracer/apps/collector.Version=v0.1.31).
var Version = "unknown"
