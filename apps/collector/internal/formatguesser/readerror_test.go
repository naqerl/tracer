package formatguesser

import (
	"context"
	"errors"
	"strings"
	"testing"

	"tracer/apps/collector/internal/parser"
)

// failingSourceSeeker fails every read with a fixed error, standing in for a
// source that exists but cannot be read, such as a kubectl source whose
// kubeconfig the service user cannot open.
type failingSourceSeeker struct {
	err error
}

func (s *failingSourceSeeker) Open(context.Context) error { return nil }

func (s *failingSourceSeeker) Next(context.Context) (string, error) { return "", s.err }

func (s *failingSourceSeeker) Close() error { return nil }

func (s *failingSourceSeeker) Seek(int64, int) (int64, error) { return 0, nil }

// TestBuildParserSpecKeepsReadFailure proves a read failure surfaces its own
// reason instead of being reported as an empty source. Retrying a smaller seek
// window and then giving up with "no logs found" is what buried the real error.
func TestBuildParserSpecKeepsReadFailure(t *testing.T) {
	readErr := errors.New(`kubectl logs (default/deploy/barse) exited: exit status 1: error loading config file "/etc/rancher/k3s/k3s.yaml": permission denied`)

	_, err := buildParserSpec(context.Background(), &failingSourceSeeker{err: readErr}, parser.Spec{})

	if err == nil {
		t.Fatal("expected an error")
	}
	if errors.Is(err, ErrNoLogs) {
		t.Fatal("read failure was misreported as an empty source")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("expected the underlying reason in %q", err.Error())
	}
}

// TestBuildParserSpecEmptySourceIsNoLogs keeps a genuinely empty source
// distinguishable from one that could not be read.
func TestBuildParserSpecEmptySourceIsNoLogs(t *testing.T) {
	_, err := buildParserSpec(context.Background(), &stubSourceSeeker{}, parser.Spec{})

	if !errors.Is(err, ErrNoLogs) {
		t.Fatalf("expected ErrNoLogs for an empty source, got %v", err)
	}
}

// eofThenFailSourceSeeker reports io.EOF on the seek probe and a real error
// afterwards, so the retry loop must still surface the failure.
type eofThenFailSourceSeeker struct {
	reads int
	err   error
}

func (s *eofThenFailSourceSeeker) Open(context.Context) error { return nil }

func (s *eofThenFailSourceSeeker) Next(context.Context) (string, error) {
	s.reads++
	return "", s.err
}

func (s *eofThenFailSourceSeeker) Close() error { return nil }

func (s *eofThenFailSourceSeeker) Seek(int64, int) (int64, error) { return 0, nil }

// TestBuildParserSpecStopsRetryingOnRealError pins that a non-EOF error ends
// the guess immediately rather than burning every seek window, which also kept
// a broken kubectl from stalling the collector's event loop.
func TestBuildParserSpecStopsRetryingOnRealError(t *testing.T) {
	src := &eofThenFailSourceSeeker{err: errors.New("permission denied")}

	_, err := buildParserSpec(context.Background(), src, parser.Spec{})

	if err == nil {
		t.Fatal("expected an error")
	}
	if src.reads != 1 {
		t.Fatalf("expected the read to be attempted once, got %d attempts", src.reads)
	}
}
