package source

import (
	"bufio"
	"os"
	"strings"
	"sync"
)

// FileConfig contains configuration for file source.
type FileConfig struct {
	Path     string
	ReadFrom string
}

// fileSource reads from a log file.
type fileSource struct {
	path string
	from string // "start" or "end"

	file   *os.File
	reader *bufio.Reader

	partial string
	offset  int64

	watchFD int
	dirWD   int
	fileWD  int
	cancelR int
	cancelW int
	events  chan struct{}
	errs    chan error
	watchWG sync.WaitGroup
}

const (
	readFromStart = "start"
	readFromEnd   = "end"
)

// NewFile creates a new file source.
func NewFile(cfg FileConfig) Source {
	return &fileSource{
		path:    cfg.Path,
		from:    strings.ToLower(strings.TrimSpace(cfg.ReadFrom)),
		watchFD: -1,
		cancelR: -1,
		cancelW: -1,
	}
}
