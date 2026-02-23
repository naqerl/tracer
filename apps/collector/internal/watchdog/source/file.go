package source

import (
	"bufio"
	"context"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ai-shift/tracer/libs/types"
)

// fileSource reads from a log file.
type fileSource struct {
	app    string
	path   string
	from   string // "start" or "end"
	file   *os.File
	offset atomic.Int64
	errCh  chan error
	mu     sync.Mutex
}

// NewFile creates a new file source.
// Enforces creation of the error channel for health monitoring.
func NewFile(app, path, from string) Source {
	return &fileSource{
		app:   app,
		path:  path,
		from:  from,
		errCh: make(chan error, 1),
	}
}

func (fs *fileSource) Name() string    { return fs.path }
func (fs *fileSource) AppName() string { return fs.app }

// Health returns the error channel. Read loop sends errors here.
func (fs *fileSource) Health() <-chan error {
	return fs.errCh
}

// Restart closes and reopens the file.
func (fs *fileSource) Restart() error {
	fs.close()
	return fs.open()
}

func (fs *fileSource) Read(ctx context.Context) <-chan types.RawLine {
	out := make(chan types.RawLine)
	go fs.readLoop(ctx, out)
	return out
}

func (fs *fileSource) readLoop(ctx context.Context, out chan<- types.RawLine) {
	defer close(out)

	if err := fs.open(); err != nil {
		fs.sendError(err)
		return
	}
	defer fs.close()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	buf := make([]byte, 0, 64*1024)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := fs.readBatch(out, buf); err != nil {
				fs.sendError(err)
				return
			}
		}
	}
}

func (fs *fileSource) sendError(err error) {
	select {
	case fs.errCh <- err:
	default:
		// Channel full or no receiver, drop
	}
}

func (fs *fileSource) open() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	file, err := os.Open(fs.path)
	if err != nil {
		return err
	}

	offset := int64(0)
	if fs.from == readFromEnd {
		offset, err = file.Seek(0, io.SeekEnd)
		if err != nil {
			file.Close()
			return err
		}
	}

	fs.file = file
	fs.offset.Store(offset)
	return nil
}

func (fs *fileSource) readBatch(out chan<- types.RawLine, buf []byte) error {
	fs.mu.Lock()
	file := fs.file
	fs.mu.Unlock()

	if file == nil {
		if err := fs.open(); err != nil {
			return err
		}
		file = fs.file
	}

	info, err := file.Stat()
	if err != nil {
		fs.reopen()
		return nil
	}

	fs.mu.Lock()
	currentOffset := fs.offset.Load()
	fs.mu.Unlock()

	if info.Size() < currentOffset {
		fs.reopen()
		return nil
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(buf, maxLineBytes)

	count := 0
	for scanner.Scan() {
		line := scanner.Text()
		if line != "" {
			select {
			case out <- types.RawLine{App: fs.app, Line: line}:
			default:
				// Channel full, drop
			}
		}
		count++
		if count >= maxBatchSize {
			break
		}
	}

	newOffset, _ := file.Seek(0, io.SeekCurrent)
	fs.offset.Store(newOffset)

	return nil
}

func (fs *fileSource) reopen() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.file != nil {
		fs.file.Close()
		fs.file = nil
	}
	fs.offset.Store(0)
}

func (fs *fileSource) close() {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if fs.file != nil {
		fs.file.Close()
		fs.file = nil
	}
}
