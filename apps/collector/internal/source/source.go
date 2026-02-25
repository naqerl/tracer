// Package source provides log source implementations.
package source

import (
	"context"
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
	panic("not implemented")
}
