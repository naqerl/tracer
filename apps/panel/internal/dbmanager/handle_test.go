package dbmanager

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"git.naqerl.com/goext/rwdb"
	_ "modernc.org/sqlite"
)

// newTestDB creates an in-memory SQLite DB and returns a *rwdb.DBHandle.
func newTestDB(t *testing.T) *rwdb.DBHandle {
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

	return rwdb.New(d)
}

// TestReadWrite exercises basic Read and Write operations.
func TestReadWrite(t *testing.T) {
	h := newTestDB(t)
	ctx := context.Background()

	// Test Write
	err := h.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(wctx, "INSERT INTO test (val) VALUES ('hello')")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Test Read
	var val string
	ro := h.Read(ctx)
	err = ro.QueryRowContext(ctx, "SELECT val FROM test WHERE id=1").Scan(&val)
	if err != nil {
		t.Fatal(err)
	}
	if val != "hello" {
		t.Fatalf("expected hello, got %s", val)
	}
}

// TestConcurrentWrites ensures writes are serialized.
func TestConcurrentWrites(t *testing.T) {
	h := newTestDB(t)
	ctx := context.Background()

	err := h.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(wctx, "INSERT INTO test (id, val) VALUES (1, 'initial')")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	const goroutines = 20
	const increments = 50

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < increments; j++ {
				err := h.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
					var current int
					err := tx.QueryRowContext(wctx, "SELECT COUNT(*) FROM test").Scan(&current)
					if err != nil {
						return err
					}
					_, err = tx.ExecContext(wctx, "UPDATE test SET val = 'updated' WHERE id=1")
					return err
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestWriteReadConcurrency tests that reads work while writes are happening.
func TestWriteReadConcurrency(t *testing.T) {
	h := newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				h.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(wctx, "INSERT INTO test (val) VALUES (?)", i)
					return err
				})
				time.Sleep(1 * time.Millisecond)
			}
		}()
	}

	// Readers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				ro := h.Read(ctx)
				rows, err := ro.QueryContext(ctx, "SELECT COUNT(*) FROM test")
				if err != nil {
					return
				}
				rows.Close()
				time.Sleep(1 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
}

// TestDBHandleClose tests that Close works and pending writes are rejected.
func TestDBHandleClose(t *testing.T) {
	h := newTestDB(t)
	ctx := context.Background()

	// Close the handle
	h.Close()

	// Write after close should fail
	err := h.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(wctx, "INSERT INTO test (val) VALUES ('fail')")
		return err
	})
	if err == nil {
		t.Fatal("expected error writing to closed handle")
	}
}
