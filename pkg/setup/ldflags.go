// Package setup provides shared installation infrastructure for tracer services.
package setup

// These variables can be overridden via -ldflags at build time.
var (
	TracerGroup   = "tracer"
	Bindir        = "/usr/local/bin"
	WithSystemd   = "auto" // "yes", "no", or "auto" (detect at runtime)
	PanelUser     = "tracer-panel"
	CollectorUser = "tracer-collector"
)
