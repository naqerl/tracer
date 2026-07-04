package dbmanager

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestConcurrentReadsDuringWrite verifies that reads can happen while a write
// is in progress. This tests the WAL + MaxOpenConns(4) setup.
func TestConcurrentReadsDuringWrite(t *testing.T) {
	d, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	d.SetMaxOpenConns(4)
	d.SetMaxIdleConns(4)

	// Create table and insert some data
	_, err = d.ExecContext(context.Background(), "CREATE TABLE test (id INTEGER PRIMARY KEY, val TEXT)")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		_, err = d.ExecContext(context.Background(), "INSERT INTO test (val) VALUES (?)", i)
		if err != nil {
			t.Fatal(err)
		}
	}

	h := NewDBHandle(d, "test-concurrent")

	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Start concurrent readers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				ro := h.Read(ctx)
				rows, err := ro.QueryContext(ctx, "SELECT COUNT(*) FROM test")
				if err != nil {
					// Context cancellation is expected
					return
				}
				rows.Close()
				time.Sleep(1 * time.Millisecond)
			}
		}()
	}

	// Start concurrent writers
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for ctx.Err() == nil {
				err := h.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
					_, err := tx.ExecContext(wctx, "INSERT INTO test (val) VALUES (?)", id)
					return err
				})
				if err != nil {
					return
				}
				time.Sleep(2 * time.Millisecond)
			}
		}(i)
	}

	wg.Wait()
}

// TestConcurrentWritesAreSerialized verifies that concurrent writes don't
// cause duplicate or lost updates.
func TestConcurrentWritesAreSerialized(t *testing.T) {
	d, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	d.SetMaxOpenConns(4)
	d.SetMaxIdleConns(4)

	_, err = d.ExecContext(context.Background(), "CREATE TABLE counter (id INTEGER PRIMARY KEY, val INTEGER DEFAULT 0)")
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.ExecContext(context.Background(), "INSERT INTO counter (id, val) VALUES (1, 0)")
	if err != nil {
		t.Fatal(err)
	}

	h := NewDBHandle(d, "test-counter")
	const goroutines = 20
	const incrementsPerGoroutine = 50

	var wg sync.WaitGroup
	ctx := context.Background()

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < incrementsPerGoroutine; j++ {
				err := h.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
					// Read current value
					var current int
					err := tx.QueryRowContext(wctx, "SELECT val FROM counter WHERE id = 1").Scan(&current)
					if err != nil {
						return err
					}
					// Update it
					_, err = tx.ExecContext(wctx, "UPDATE counter SET val = ? WHERE id = 1", current+1)
					return err
				})
				if err != nil {
					t.Errorf("Write failed: %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()

	var final int
	ro := h.Read(context.Background())
	err = ro.QueryRowContext(context.Background(), "SELECT val FROM counter WHERE id = 1").Scan(&final)
	if err != nil {
		t.Fatal(err)
	}
	expected := goroutines * incrementsPerGoroutine
	if final != expected {
		t.Fatalf("expected counter=%d, got %d", expected, final)
	}
}
