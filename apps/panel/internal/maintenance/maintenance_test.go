package maintenance

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
)

func TestRunOnceReclaimsDeletedPages(t *testing.T) {
	mgr := newTestManager(t)
	handle := createApp(t, mgr, "collector", "app")
	seedLogs(t, handle, 4000, 2048)
	truncateWAL(t, handle)
	deleteLogs(t, handle)

	path := mgr.AppDBPath("collector", "app")
	before := fileBytes(t, path)

	svc := New(mgr, DefaultConfig())
	svc.RunOnce(t.Context())

	res, ok := svc.LastResult("collector", "app")
	if !ok {
		t.Fatal("no maintenance result recorded")
	}
	if !res.Vacuumed {
		t.Fatalf("expected vacuum, got result %+v", res)
	}
	if res.ReclaimedBytes <= 0 {
		t.Fatalf("expected reclaimed bytes > 0, got %d", res.ReclaimedBytes)
	}
	if got := res.WALBytesAfter; got != 0 {
		t.Fatalf("expected WAL bytes after = 0, got %d", got)
	}
	if got := fileBytes(t, path+"-wal"); got != 0 {
		t.Fatalf("expected -wal file to be truncated to 0 bytes, got %d", got)
	}

	after := fileBytes(t, path)
	if after >= before {
		t.Fatalf("expected database file to shrink, before=%d after=%d", before, after)
	}
}

func TestRunOnceSkipsVacuumBelowThreshold(t *testing.T) {
	mgr := newTestManager(t)
	handle := createApp(t, mgr, "collector", "app")
	seedLogs(t, handle, 20, 64)
	truncateWAL(t, handle)

	path := mgr.AppDBPath("collector", "app")
	old := fileModifiedTime(t, path).Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("set database mtime: %v", err)
	}

	svc := New(mgr, DefaultConfig())
	svc.RunOnce(t.Context())

	res, ok := svc.LastResult("collector", "app")
	if !ok {
		t.Fatal("no maintenance result recorded")
	}
	if res.Vacuumed {
		t.Fatalf("expected no vacuum below the free-share threshold, got result %+v", res)
	}
	if res.WALBytesAfter != 0 {
		t.Fatalf("expected the checkpoint to leave WAL bytes at 0, got %d", res.WALBytesAfter)
	}
	if got := fileModifiedTime(t, path); !got.Equal(old) {
		t.Fatalf("expected database mtime to stay unchanged, before=%v after=%v", old, got)
	}
}

func TestConcurrentWriterDuringPass(t *testing.T) {
	mgr := newTestManager(t)
	handle := createApp(t, mgr, "collector", "app")
	seedLogs(t, handle, 4000, 2048)
	truncateWAL(t, handle)
	deleteLogs(t, handle)

	svc := New(mgr, DefaultConfig())

	passDone := make(chan struct{})
	go func() {
		defer close(passDone)
		svc.RunOnce(t.Context())
	}()

	for i := 0; i < 200; i++ {
		err := handle.Write(t.Context(), func(tx db.DBTX) error {
			_, err := tx.ExecContext(t.Context(), "INSERT INTO log (level, message, context, created_at) VALUES ('info', 'during maintenance', '{}', 0)")
			return err
		})
		if err != nil {
			t.Fatalf("concurrent write during maintenance failed: %v", err)
		}
	}

	<-passDone
}

func TestRunOnceSkipsCorruptDatabaseAndContinues(t *testing.T) {
	mgr := newTestManager(t)
	createApp(t, mgr, "collector", "good")

	appsDir := filepath.Dir(mgr.AppDBPath("collector", "good"))
	if err := os.WriteFile(filepath.Join(appsDir, "broken.db"), []byte("not a sqlite database"), 0644); err != nil {
		t.Fatalf("write corrupt database: %v", err)
	}

	svc := New(mgr, DefaultConfig())
	svc.RunOnce(t.Context())

	if _, ok := svc.LastResult("collector", "broken"); ok {
		t.Fatal("corrupt database must not record a successful result")
	}
	if _, ok := svc.LastResult("collector", "good"); !ok {
		t.Fatal("pass did not continue to the next app after the corrupt database")
	}
}

func newTestManager(t *testing.T) *dbmanager.Manager {
	t.Helper()

	mgr, err := dbmanager.New(t.TempDir())
	if err != nil {
		t.Fatalf("create dbmanager: %v", err)
	}
	t.Cleanup(func() {
		if err := mgr.Close(); err != nil {
			t.Errorf("close dbmanager: %v", err)
		}
	})
	return mgr
}

func createApp(t *testing.T, mgr *dbmanager.Manager, collectorName, appName string) *dbmanager.DBHandle {
	t.Helper()

	handle, err := mgr.CreateAppDB(collectorName, appName)
	if err != nil {
		t.Fatalf("create app database: %v", err)
	}
	return handle
}

func seedLogs(t *testing.T, handle *dbmanager.DBHandle, rows, size int) {
	t.Helper()

	message := strings.Repeat("x", size)
	err := handle.Write(t.Context(), func(tx db.DBTX) error {
		stmt, err := tx.PrepareContext(t.Context(), "INSERT INTO log (level, message, context, created_at) VALUES ('info', ?, '{}', 0)")
		if err != nil {
			return err
		}
		defer stmt.Close()

		for i := 0; i < rows; i++ {
			if _, err := stmt.ExecContext(t.Context(), message); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed logs: %v", err)
	}
}

func deleteLogs(t *testing.T, handle *dbmanager.DBHandle) {
	t.Helper()

	err := handle.Write(t.Context(), func(tx db.DBTX) error {
		_, err := tx.ExecContext(t.Context(), "DELETE FROM log")
		return err
	})
	if err != nil {
		t.Fatalf("delete logs: %v", err)
	}
}

func truncateWAL(t *testing.T, handle *dbmanager.DBHandle) {
	t.Helper()

	err := handle.Exclusive(t.Context(), func(tx db.DBTX) error {
		var busy, logFrames, checkpointed int64
		return tx.QueryRowContext(t.Context(), "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed)
	})
	if err != nil {
		t.Fatalf("truncate wal: %v", err)
	}
}

func fileBytes(t *testing.T, path string) int64 {
	t.Helper()

	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}

func fileModifiedTime(t *testing.T, path string) time.Time {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.ModTime()
}
