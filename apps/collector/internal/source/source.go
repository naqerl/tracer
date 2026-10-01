// Package source provides log source implementations.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNotFound reports that the source itself does not exist: no such systemd
// unit, no such container, or an unreadable path. It is distinct from a source
// that exists but could not be read, so a missing permission or a broken
// kubeconfig is never reported as an absent service.
var ErrNotFound = errors.New("source not found")

// NotFound marks err as ErrNotFound so callers can tell an absent source from
// a source that failed to produce logs for another reason.
func NotFound(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrNotFound, err)
}

type Spec struct {
	Type   string            `yaml:"type"`
	Params map[string]string `yaml:"params"`
}

// Source provides a stream of log lines from a log source.
type Source interface {
	Open(ctx context.Context) error
	Next(ctx context.Context) (line string, err error)
	Close() error
}

// Implements io.Seeker for position-based seeking.
// Position semantics:
//   - FileSource: Byte offset from file start (standard file position)
//   - JournalctlSource: Opaque position (always returns 0). Supports:
//   - Seek(0, io.SeekStart): From beginning (all history)
//   - Seek(0, io.SeekEnd): From now (tail mode)
//   - Seek(-N, io.SeekEnd): Last N lines, then follow
type SourceSeeker interface {
	Source
	io.Seeker
}

// New creates a new Source from the given spec.
// Returns an error if the source type is unknown or required parameters are missing.
func New(spec Spec) (Source, error) {
	t := strings.ToLower(strings.TrimSpace(spec.Type))
	switch t {
	case "file":
		path, err := getParam(spec, "path")
		if err != nil {
			return nil, fmt.Errorf("file source: %w", err)
		}
		readFrom, err := getParam(spec, "read_from")
		if err != nil {
			return nil, fmt.Errorf("file source: %w", err)
		}
		return NewFile(FileConfig{
			Path:     path,
			ReadFrom: readFrom,
		}), nil
	case "journalctl":
		unit, err := getParam(spec, "unit")
		if err != nil {
			return nil, fmt.Errorf("journalctl source: %w", err)
		}
		return NewJournalctl(JournalctlConfig{
			Unit: unit,
		}), nil
	case "kubectl":
		resource, err := getParam(spec, "resource")
		if err != nil {
			return nil, fmt.Errorf("kubectl source: %w", err)
		}
		return NewKubectl(KubectlConfig{
			Resource:   resource,
			Namespace:  getParamDefault(spec, "namespace", "default"),
			Container:  getParamDefault(spec, "container", ""),
			Kubeconfig: getParamDefault(spec, "kubeconfig", ""),
		}), nil
	default:
		return nil, fmt.Errorf("source: unknown source type %q", spec.Type)
	}
}

// getParam retrieves a parameter from the spec, returning an error if missing.
func getParam(spec Spec, key string) (string, error) {
	if spec.Params == nil {
		return "", fmt.Errorf("missing params.%s", key)
	}
	if v := strings.TrimSpace(spec.Params[key]); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("missing params.%s", key)
}

// getParamDefault retrieves a parameter from the spec, returning a default if missing or empty.
func getParamDefault(spec Spec, key, def string) string {
	if spec.Params == nil {
		return def
	}
	if v := strings.TrimSpace(spec.Params[key]); v != "" {
		return v
	}
	return def
}
