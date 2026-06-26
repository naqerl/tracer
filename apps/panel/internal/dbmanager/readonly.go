package dbmanager

import (
	"context"
	"database/sql"
	"errors"

	"tracer/apps/panel/db"
)

// ErrReadOnly is returned when a write operation is attempted on a read-only handle.
var ErrReadOnly = errors.New("db handle is read-only")

// readOnlyDB wraps a *sql.DB and rejects write operations.
// Implements db.DBTX for use with sqlc-generated read queries.
//
// NOTE: PrepareContext is passed through to the underlying pool.
// If a caller prepares a write statement (INSERT/UPDATE/DELETE) and
// then calls Stmt.Exec(), that write WILL bypass the read-only guard.
// sqlc-generated code uses ExecContext (not Prepare+Exec) for writes,
// so this is not a concern in practice. Still, callers should not use
// prepared statements for write operations on a readOnlyDB.
type readOnlyDB struct {
	pool *sql.DB
}

func (r *readOnlyDB) ExecContext(_ context.Context, _ string, _ ...interface{}) (sql.Result, error) {
	return nil, ErrReadOnly
}

func (r *readOnlyDB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return r.pool.PrepareContext(ctx, query)
}

func (r *readOnlyDB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return r.pool.QueryContext(ctx, query, args...)
}

func (r *readOnlyDB) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return r.pool.QueryRowContext(ctx, query, args...)
}

// Compile-time interface check
var _ db.DBTX = (*readOnlyDB)(nil)
