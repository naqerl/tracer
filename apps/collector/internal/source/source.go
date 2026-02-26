// Package source provides log source implementations.
package source

import (
	"context"
	"fmt"
	"strings"
)

type Spec struct {
	Type   string            `yaml:"type"`
	Params map[string]string `yaml:"params"`
}

// Source provides a stream of log lines from a log source.
// Implementations may optionally implement io.Seeker for position-based seeking.
//
// Position semantics:
//   - FileSource: Byte offset from file start (standard file position)
//   - JournalctlSource: Opaque position (always returns 0). Supports:
//     - Seek(0, io.SeekStart): From beginning (all history)
//     - Seek(0, io.SeekEnd): From now (tail mode)
//     - Seek(-N, io.SeekEnd): Last N lines, then follow
type Source interface {
	Open(ctx context.Context) error
	Next(ctx context.Context) (line string, err error)
	Close() error
}

func New(spec Spec) Source {
	t := strings.ToLower(strings.TrimSpace(spec.Type))
	switch t {
	case "file":
		return NewFile(FileConfig{
			Path:     requiredParam(spec, "path"),
			ReadFrom: requiredParam(spec, "read_from"),
		})
	case "journalctl":
		return NewJournalctl(JournalctlConfig{
			Unit: requiredParam(spec, "unit"),
		})
	default:
		panic(fmt.Sprintf("source: unknown source type %q", spec.Type))
	}
}

func requiredParam(spec Spec, key string) string {
	if spec.Params == nil {
		panic(fmt.Sprintf("source: missing params.%s", key))
	}
	if v := strings.TrimSpace(spec.Params[key]); v != "" {
		return v
	}
	panic(fmt.Sprintf("source: missing params.%s", key))
}
