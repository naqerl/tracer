// Package source provides log source implementations.
package source

import (
	"context"
	"time"

	"github.com/ai-shift/tracer/libs/types"
)


type Spec struct {
        ID string `toml:"id"`
        Type string `toml:"type"`
        Params map[string]string `toml:"params"`
}
