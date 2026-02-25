// Package source provides log source implementations.
package source

import (
	"context"
	"fmt"
	"strings"
)

type Spec struct {
	ID     string            `toml:"id"`
	Type   string            `toml:"type"`
	Params map[string]string `toml:"params"`
}

type Source interface {
	Lines(ctx context.Context) <-chan string
}

func New(spec Spec) Source {
	t := strings.ToLower(strings.TrimSpace(spec.Type))
	switch t {
	case "file":
		return NewFile(FileConfig{
			AppName:  requiredParam(spec, "app_name"),
			Path:     requiredParam(spec, "path"),
			ReadFrom: requiredParam(spec, "read_from"),
		})
	case "journalctl":
		return NewJournalctl(JournalctlConfig{
			AppName: requiredParam(spec, "app_name"),
			Unit:    requiredParam(spec, "unit"),
		})
	default:
		panic(fmt.Sprintf("source %q: unknown source type %q", spec.ID, spec.Type))
	}
}

func requiredParam(spec Spec, key string) string {
	if spec.Params == nil {
		panic(fmt.Sprintf("source %q: missing params.%s", spec.ID, key))
	}
	if v := strings.TrimSpace(spec.Params[key]); v != "" {
		return v
	}
	panic(fmt.Sprintf("source %q: missing params.%s", spec.ID, key))
}
