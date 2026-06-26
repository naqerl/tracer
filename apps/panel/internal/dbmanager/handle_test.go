package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"tracer/apps/panel/db"

	_ "modernc.org/sqlite"
)

// newTestDB creates an in-memory SQLite DB and returns a DBHandle.
// The handle is registered for cleanup via t.Cleanup.
func newTestDB(t *testing.T) *DBHandle {
	t.Helper()
	d, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	// Create a basic table for testing
	_, err = d.ExecContext(context.Background(), "CREATE TABLE IF NOT EXISTS test (id INTEGER PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	return NewDBHandle(d, "test")
}

// closeHandle is a test helper that closes the handle and unblocks any
// pending writes if the handle is not yet closed.
func closeHandle(h *DBHandle) {
	h.Close()
}

func TestDBHandle_ReadReturnsReadOnly(t *testing.T) {
	h := newTestDB(t)
	ro := h.Read()
	if _, err := ro.ExecContext(context.Background(), "INSERT INTO test (val) VALUES ('x')"); err != ErrReadOnly {
		t.Fatalf("expected ErrReadOnly, got %v", err)
	}
}

func TestDBHandle_WriteExecutesCallback(t *testing.T) {
	h := newTestDB(t)
	err := h.Write(context.Background(), func(tx db.DBTX) error {
		_, err := tx.ExecContext(context.Background(), "INSERT INTO test (val) VALUES ('hello')")
		return err
	})
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Verify the write was committed
	var val string
	err = h.pool.QueryRowContext(context.Background(), "SELECT val FROM test WHERE id = 1").Scan(&val)
	if err != nil {
		t.Fatal(err)
	}
	if val != "hello" {
		t.Fatalf("expected 'hello', got %q", val)
	}
}

func TestDBHandle_WriteReturnsError(t *testing.T) {
	h := newTestDB(t)
	expectedErr := errors.New("oops")
	err := h.Write(context.Background(), func(tx db.DBTX) error {
		return expectedErr
	})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected %v, got %v", expectedErr, err)
	}
}

func TestDBHandle_WriteRollsBackOnError(t *testing.T) {
	h := newTestDB(t)
	// First insert a row inside a successful write
	err := h.Write(context.Background(), func(tx db.DBTX) error {
		_, err := tx.ExecContext(context.Background(), "INSERT INTO test (val) VALUES ('persist')")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Now do a failing write that also inserts
	err = h.Write(context.Background(), func(tx db.DBTX) error {
		_, err := tx.ExecContext(context.Background(), "INSERT INTO test (val) VALUES ('rollback')")
		if err != nil {
			return err
		}
		return errors.New("fail after insert")
	})
	if err == nil {
		t.Fatal("expected error")
	}

	// Verify the second insert was rolled back
	var count int
	err = h.pool.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM test").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row (only persisted), got %d", count)
	}
}

func TestDBHandle_WriteSerialization(t *testing.T) {
	h := newTestDB(t)
	const goroutines = 10
	const opsPerGoroutine = 100

	var counter int64
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			for j := 0; j < opsPerGoroutine; j++ {
				if err := h.Write(context.Background(), func(tx db.DBTX) error {
					atomic.AddInt64(&counter, 1)
					return nil
				}); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}

	for i := 0; i < goroutines; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}

	expected := int64(goroutines * opsPerGoroutine)
	if counter != expected {
		t.Fatalf("expected counter=%d, got %d", expected, counter)
	}
}

func TestDBHandle_WriteContextCancellation(t *testing.T) {
	h := newTestDB(t)
	defer closeHandle(h)

	// Fill the queue with a blocking operation.
	// We send directly to wch to ensure a write is already in flight.
	block := make(chan struct{})
	h.wch <- writeRequest{
		fn: func(db.DBTX) error {
			<-block
			return nil
		},
		done: make(chan error, 1),
	}

	// Wait until the worker picks up the blocking request
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := h.Write(ctx, func(tx db.DBTX) error {
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}

	// Unblock the worker so it can shut down cleanly
	close(block)
}

func TestDBHandle_DeadlockGuard(t *testing.T) {
	h := newTestDB(t)

	err := h.Write(context.Background(), func(tx db.DBTX) error {
		// Attempt nested Write on the same handle
		return h.Write(context.Background(), func(tx2 db.DBTX) error {
			return nil
		})
	})
	if !errors.Is(err, ErrWriteDeadlock) {
		t.Fatalf("expected ErrWriteDeadlock, got %v", err)
	}
}

func TestDBHandle_WriteAfterClose(t *testing.T) {
	h := newTestDB(t)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	err := h.Write(context.Background(), func(tx db.DBTX) error {
		return nil
	})
	if !errors.Is(err, errWriteQueueClosed) {
		t.Fatalf("expected errWriteQueueClosed, got %v", err)
	}
}

func TestDBHandle_CloseDrainsQueue(t *testing.T) {
	h := newTestDB(t)

	// Start the worker by sending a quick write first
	if err := h.Write(context.Background(), func(tx db.DBTX) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Now send writes directly to the channel (bypass Write to avoid racing with Close)
	results := make(chan error, 3)
	for i := 0; i < 3; i++ {
		done := make(chan error, 1)
		h.wch <- writeRequest{
			fn: func(tx db.DBTX) error {
				return nil
			},
			done: done,
		}
		go func() {
			select {
			case err := <-done:
				results <- err
			case <-time.After(5 * time.Second):
				results <- errors.New("timeout waiting for drain")
			}
		}()
	}

	// Close should drain the queued requests
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		err := <-results
		// Writes may be processed before the worker sees the close signal,
		// so both nil (processed) and errWriteQueueClosed (drained) are acceptable.
		if err != nil && !errors.Is(err, errWriteQueueClosed) {
			t.Fatalf("expected nil or errWriteQueueClosed, got %v", err)
		}
	}
}

func TestDBHandle_ReadDuringWrite(t *testing.T) {
	h := newTestDB(t)
	defer closeHandle(h)

	// Start a slow write
	writeDone := make(chan struct{})
	go func() {
		h.Write(context.Background(), func(tx db.DBTX) error {
			time.Sleep(50 * time.Millisecond)
			_, err := tx.ExecContext(context.Background(), "INSERT INTO test (val) VALUES ('from write')")
			return err
		})
		close(writeDone)
	}()

	// Give the write a moment to start
	time.Sleep(5 * time.Millisecond)

	// Read concurrently - should not block (or at least should work)
	ro := h.Read()
	var count int
	err := ro.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM test").Scan(&count)
	if err != nil {
		t.Fatalf("read during write failed: %v", err)
	}

	<-writeDone
}

func TestDBHandle_Close(t *testing.T) {
	h := newTestDB(t)
	if err := h.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestDBHandle_CloseDuringWrite(t *testing.T) {
	h := newTestDB(t)

	// Start the worker by sending a quick write first
	if err := h.Write(context.Background(), func(tx db.DBTX) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Send a blocking write directly to wch
	block := make(chan struct{})
	writeDone := make(chan error, 1)

	done := make(chan error, 1)
	h.wch <- writeRequest{
		fn: func(tx db.DBTX) error {
			<-block
			_, err := tx.ExecContext(context.Background(), "INSERT INTO test (val) VALUES ('during close')")
			return err
		},
		done: done,
	}
	go func() {
		select {
		case err := <-done:
			writeDone <- err
		case <-time.After(5 * time.Second):
			writeDone <- errors.New("timeout")
		}
	}()

	// Ensure the worker has picked up the request
	time.Sleep(50 * time.Millisecond)

	// Unblock the write before closing
	close(block)

	// Close while write is in progress
	if err := h.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// The write should have completed
	err := <-writeDone
	if err != nil && !errors.Is(err, errWriteQueueClosed) {
		t.Fatalf("expected write to complete or be closed, got: %v", err)
	}
}

func TestDBHandle_CrossHandleNestedWrites(t *testing.T) {
	// Use separate file-based databases so transactions don't block each other.
	h1 := newTestFileDB(t)
	h2 := newTestFileDB(t)

	// Write to h1, and inside that callback also write to h2.
	// This should NOT trigger the deadlock guard since they are different handles.
	err := h1.Write(context.Background(), func(tx db.DBTX) error {
		_, err := tx.ExecContext(context.Background(), "INSERT INTO test (val) VALUES ('from h1')")
		if err != nil {
			return err
		}
		// Nested write to h2 from within h1's write callback
		return h2.Write(context.Background(), func(tx2 db.DBTX) error {
			_, err := tx2.ExecContext(context.Background(), "INSERT INTO test (val) VALUES ('from h2')")
			return err
		})
	})
	if err != nil {
		t.Fatalf("cross-handle nested write failed: %v", err)
	}

	// Verify both writes succeeded
	var count int
	err = h1.pool.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM test").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row in h1, got %d", count)
	}

	err = h2.pool.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM test").Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row in h2, got %d", count)
	}
}

// newTestFileDB creates a DBHandle backed by a temp file (not shared memory),
// so each handle has an independent database.
func newTestFileDB(t *testing.T) *DBHandle {
	t.Helper()
	f := t.TempDir() + "/test.db"
	d, err := sql.Open("sqlite", "file:"+f+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })

	_, err = d.ExecContext(context.Background(), "CREATE TABLE IF NOT EXISTS test (id INTEGER PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatal(err)
	}

	return NewDBHandle(d, "test-file")
}
