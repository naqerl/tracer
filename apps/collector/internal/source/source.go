// Package source provides log source implementations.
package source

import (
	"context"
	"fmt"
	"strings"
)

type Spec struct {
	Type   string            `toml:"type"`
	Params map[string]string `toml:"params"`
}

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
