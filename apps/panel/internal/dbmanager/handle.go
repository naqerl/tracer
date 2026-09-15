package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"

	"tracer/apps/panel/db"
)

// errWriteQueueClosed is sent to callers waiting on a write when Close() drains the queue.
var errWriteQueueClosed = errors.New("write queue closed")

// goroutineID returns the current goroutine ID. Used for deadlock detection.
// NOTE: this parses runtime.Stack output, which is not covered by Go's API
// compatibility promise. If it breaks in a future Go release, replace with
// a sync.Mutex-based reentry guard (see design alternatives in handle.go).
func goroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// Parse: "goroutine <ID> [running]:"
	// Skip "goroutine "
	i := 10 // len("goroutine ") == 10
	if i >= n {
		return 0
	}
	var id int64
	for i < n && buf[i] >= '0' && buf[i] <= '9' {
		id = id*10 + int64(buf[i]-'0')
		i++
	}
	return id
}

// ErrWriteDeadlock is returned when Write() is called from within a Write()
// callback on the same DBHandle, which would deadlock the write queue.
var ErrWriteDeadlock = errors.New("write deadlock detected: nested Write() on same handle")

// writeRequest wraps a write callback for the serialized write queue.
type writeRequest struct {
	fn   func(db.DBTX) error
	done chan error
}

// DBHandle provides safe concurrent access to a SQLite database.
// Reads go through the connection pool (via Read()) and can happen concurrently
// with each other. Writes are serialized through a single-worker goroutine queue
// and executed inside a transaction for atomicity.
type DBHandle struct {
	pool *sql.DB
	name string

	wch    chan writeRequest
	once   sync.Once
	closed chan struct{}

	mu        sync.Mutex
	workerGID int64 // goroutine ID of the write worker, 0 until worker starts
	wg        sync.WaitGroup
}

// NewDBHandle creates a new DBHandle wrapping the given connection pool.
func NewDBHandle(pool *sql.DB, name string) *DBHandle {
	return &DBHandle{
		pool:   pool,
		name:   name,
		wch:    make(chan writeRequest, 64),
		closed: make(chan struct{}),
	}
}

// Read returns a read-only DBTX for this database.
// Multiple goroutines can call Read() concurrently.
// The returned value rejects ExecContext with ErrReadOnly.
// Note: PrepareContext is passed through to the underlying pool; callers
// should not use prepared statements for write operations on the read-only handle.
func (h *DBHandle) Read() *readOnlyDB {
	return &readOnlyDB{pool: h.pool}
}

// Write executes the given function on the write queue for this database.
// The function receives a *sql.Tx so that all operations within fn are atomic.
// Only one Write callback executes at a time per DBHandle.
// Returns ErrWriteDeadlock if Write is called from within a Write callback
// on the same handle (to prevent a deadlock).
// Returns an error if the handle has been closed.
func (h *DBHandle) Write(ctx context.Context, fn func(db.DBTX) error) error {
	// Deadlock guard: if we're already on the write worker goroutine,
	// then Write() is being called from within a Write() callback,
	// which would deadlock because the worker is blocked on the callback.
	h.mu.Lock()
	workerGID := h.workerGID
	closed := h.isClosed()
	h.mu.Unlock()

	if closed {
		return errWriteQueueClosed
	}

	if workerGID != 0 && goroutineID() == workerGID {
		return fmt.Errorf("db %s: %w", h.name, ErrWriteDeadlock)
	}

	return h.submit(ctx, fn)
}

// isClosed returns true if the handle has been closed.
func (h *DBHandle) isClosed() bool {
	select {
	case <-h.closed:
		return true
	default:
		return false
	}
}

// submit sends the write request to the queue and waits for completion.
func (h *DBHandle) submit(ctx context.Context, fn func(db.DBTX) error) error {
	// Ensure the worker goroutine is running
	h.once.Do(func() {
		h.wg.Go(h.workerLoop)
	})

	done := make(chan error, 1)

	select {
	case h.wch <- writeRequest{fn: fn, done: done}:
	case <-h.closed:
		return errWriteQueueClosed
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-done:
		return err
	case <-h.closed:
		return errWriteQueueClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// workerLoop processes write requests sequentially.
func (h *DBHandle) workerLoop() {
	h.mu.Lock()
	h.workerGID = goroutineID()
	h.mu.Unlock()

	slog.Debug("write worker started", "db", h.name, "gid", h.workerGID)
	defer slog.Debug("write worker stopped", "db", h.name)

	for {
		select {
		case req := <-h.wch:
			h.processRequest(req)
		case <-h.closed:
			// Drain remaining requests so callers don't hang.
			h.drainRequests()
			return
		}
	}
}

// drainRequests reads any remaining write requests from the channel and
// sends them a closed error so callers don't hang indefinitely.
func (h *DBHandle) drainRequests() {
	for {
		select {
		case req := <-h.wch:
			select {
			case req.done <- errWriteQueueClosed:
			default:
			}
		default:
			return
		}
	}
}

// processRequest executes a single write request inside a transaction.
func (h *DBHandle) processRequest(req writeRequest) {
	// Begin a transaction for atomicity
	tx, err := h.pool.BeginTx(context.Background(), nil)
	if err != nil {
		select {
		case req.done <- err:
		default:
		}
		return
	}

	err = req.fn(tx)
	if err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			slog.Error("write rollback failed", "db", h.name, "rollback_err", rbErr, "original_err", err)
		}
		select {
		case req.done <- err:
		default:
		}
		return
	}

	if err := tx.Commit(); err != nil {
		select {
		case req.done <- err:
		default:
		}
		return
	}

	select {
	case req.done <- nil:
	default:
	}
}

// DrainAndClose waits for all writes already submitted to the queue to
// complete, then shuts down the worker and closes the pool. Unlike Close, it
// drops no in-flight write: every request queued before the call is committed
// before the pool is closed. Writes submitted after the call fail with
// errWriteQueueClosed.
func (h *DBHandle) DrainAndClose() error {
	h.mu.Lock()
	workerGID := h.workerGID
	closed := h.isClosed()
	h.mu.Unlock()

	if workerGID != 0 && goroutineID() == workerGID {
		return fmt.Errorf("db %s: %w", h.name, ErrWriteDeadlock)
	}
	if !closed {
		h.once.Do(func() {
			h.wg.Go(h.workerLoop)
		})

		done := make(chan error, 1)
		select {
		case h.wch <- writeRequest{fn: func(db.DBTX) error { return nil }, done: done}:
		case <-h.closed:
		}
		select {
		case <-done:
		case <-h.closed:
		}
	}
	return h.Close()
}

// Close shuts down the write worker and closes the pool.
// It waits for any in-flight write to complete before closing the pool.
// Pending queued writes receive ErrWriteQueueClosed. Subsequent Write calls
// return an error immediately.
func (h *DBHandle) Close() error {
	select {
	case <-h.closed:
		// Already closed
	default:
		close(h.closed)
	}
	// Wait for the worker to finish any in-flight work.
	h.wg.Wait()
	return h.pool.Close()
}
