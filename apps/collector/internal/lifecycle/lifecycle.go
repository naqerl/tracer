package lifecycle

import (
	"fmt"
	"regexp"
	"strings"

	collectorconfig "tracer/apps/collector/internal/config"
	"tracer/libs/types"
)

// Detector identifies lifecycle events from raw log lines.
// Zero-value detector is a noop.
type Detector struct {
	start   *regexp.Regexp
	restart *regexp.Regexp
	exit    *regexp.Regexp
}

// New creates a lifecycle detector from app lifecycle config.
func New(spec collectorconfig.LifecycleSpec) (Detector, error) {
	var d Detector
	if err := d.RegisterPattern(types.LifecycleStart, spec.StartPattern); err != nil {
		return Detector{}, err
	}
	if err := d.RegisterPattern(types.LifecycleRestart, spec.RestartPattern); err != nil {
		return Detector{}, err
	}
	if err := d.RegisterPattern(types.LifecycleExit, spec.ExitPattern); err != nil {
		return Detector{}, err
	}
	return d, nil
}

// RegisterPattern compiles and registers a lifecycle detection regex.
// Empty patterns are ignored, so zero-value detector remains a noop.
func (d *Detector) RegisterPattern(kind types.LifecycleKind, pattern string) error {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	if err := kind.Validate(); err != nil {
		return err
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("compile %s pattern: %w", kind, err)
	}

	switch kind {
	case types.LifecycleStart:
		d.start = re
	case types.LifecycleRestart:
		d.restart = re
	case types.LifecycleExit:
		d.exit = re
	}
	return nil
}

func (d Detector) Detect(line string) (types.LifecycleKind, bool) {
	switch {
	case d.restart != nil && d.restart.MatchString(line):
		return types.LifecycleRestart, true
	case d.start != nil && d.start.MatchString(line):
		return types.LifecycleStart, true
	case d.exit != nil && d.exit.MatchString(line):
		return types.LifecycleExit, true
	default:
		return "", false
	}
}
