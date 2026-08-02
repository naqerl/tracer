// Package testutil provides shared helpers for panel tests: an in-memory
// panel database with migrations applied, a dbmanager whose app databases
// skip fsync, and a fixed master key. Every panel test package uses these so
// the suite avoids duplicating the SQLite setup boilerplate.
package testutil

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"tracer/apps/panel/internal/dbmanager"
	panelmigrations "tracer/apps/panel/sql/panel"
)

// TestKeySecret returns a fixed 32-byte master key for tests.
func TestKeySecret() []byte {
	return []byte("0123456789abcdef0123456789abcdef")
}

// NewTestDB opens an in-memory panel database with all migrations applied.
// Each test gets a unique database so parallel or repeated runs cannot share
// state through sqlite's shared cache.
func NewTestDB(t testing.TB) *dbmanager.DBHandle {
	t.Helper()

	name := strings.ReplaceAll(t.Name(), "/", "_")
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared", name))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, panelmigrations.Embed)
	if err != nil {
		t.Fatalf("create goose provider: %v", err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	return dbmanager.NewDBHandle(db, "test-panel")
}

// NewTestDBManager creates a dbmanager.Manager backed by a temp directory.
// Its databases are written with synchronous=OFF and a MEMORY journal so the
// suite does not pay per-test fsync latency.
func NewTestDBManager(t testing.TB) *dbmanager.Manager {
	t.Helper()

	dir := t.TempDir()
	mgr, err := dbmanager.NewWithDSN(dir, func(dbPath string) string {
		return fmt.Sprintf("file:%s?_pragma=synchronous(0)&_pragma=journal_mode(MEMORY)", dbPath)
	})
	if err != nil {
		t.Fatalf("create dbmanager: %v", err)
	}
	return mgr
}
