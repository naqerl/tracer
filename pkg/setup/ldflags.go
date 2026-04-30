// Package setup provides shared installation infrastructure for tracer services.
package setup

// These variables are set via -ldflags at build time from config.mk values.
var (
	TracerGroup   = "tracer"
	Bindir        = "/usr/local/bin"
	WithSystemd   = "no"
	PanelUser     = "tracer-panel"
	CollectorUser = "tracer-collector"
)
