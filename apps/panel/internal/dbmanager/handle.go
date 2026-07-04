package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"git.naqerl.com/goext/rwdb"
	"github.com/pressly/goose/v3"

	"tracer/apps/panel/db"
	panelmigrations "tracer/apps/panel/sql/panel"
)

// DBHandle wraps *rwdb.DBHandle for backward compatibility.
// It provides the same Write/Read API with the new rwdb backend.
type DBHandle struct {
	inner *rwdb.DBHandle
	name  string
}

// NewDBHandle creates a new DBHandle wrapping the given connection pool.
func NewDBHandle(pool *sql.DB, name string) *DBHandle {
	return &DBHandle{
		inner: rwdb.New(pool),
		name:  name,
	}
}

// Read returns a read-only DBTX for this database.
// If called from within a Write callback (detected by ctx), it returns
// the active transaction instead, so queries share the same transaction.
func (h *DBHandle) Read(ctx context.Context) db.DBTX {
	return h.inner.Read(ctx)
}

// Write executes the given function on the write queue for this database.
// The function receives a context (with the active transaction stored inside)
// and the *sql.Tx so that all operations within fn are atomic.
// Only one Write callback executes at a time per DBHandle.
func (h *DBHandle) Write(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	return h.inner.Write(ctx, fn)
}

// Close shuts down the write worker and closes the pool.
func (h *DBHandle) Close() error {
	return h.inner.Close()
}

// openPanelDB opens the panel database, runs migrations, and returns an *DBHandle.
func openPanelDB(dataDir string) (*DBHandle, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "panel.db")
	dsn := buildDSN(dbPath)

	sqlDB, err := openSQLiteDB(dsn)
	if err != nil {
		return nil, fmt.Errorf("open panel database: %w", err)
	}

	if err := runPanelMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run panel migrations: %w", err)
	}

	return NewDBHandle(sqlDB, "panel"), nil
}

func runPanelMigrations(sqlDB *sql.DB) error {
	gooseProvider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, panelmigrations.Embed)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	if _, err := gooseProvider.Up(context.Background()); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// ErrWriteDeadlock is reported when a deadlock is detected.
// NOTE: rwdb handles nested writes gracefully, so this error is no longer
// returned by Write. It is kept for backward compatibility.
var ErrWriteDeadlock = errors.New("write deadlock detected")

// ErrReadOnly is returned when a write operation is attempted on a read-only handle.
var ErrReadOnly = rwdb.ErrReadOnly
