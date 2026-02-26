package dbmanager

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"tracer/apps/panel/db"
)

// loggingDB wraps a DBTX and logs query timings
type loggingDB struct {
	db   db.DBTX
	name string
}

// newLoggingDB creates a new logging database wrapper
func newLoggingDB(db db.DBTX, name string) *loggingDB {
	return &loggingDB{db: db, name: name}
}

func (l *loggingDB) logQuery(ctx context.Context, operation, query string, start time.Time, err error) {
	duration := time.Since(start)
	level := slog.LevelDebug
	if duration > 50*time.Millisecond {
		level = slog.LevelWarn
	}

	attrs := []any{
		"db", l.name,
		"operation", operation,
		"duration_ms", duration.Milliseconds(),
		"duration", duration.String(),
	}
	if query != "" {
		attrs = append(attrs, "query", query)
	}
	if err != nil && err != sql.ErrNoRows {
		attrs = append(attrs, "error", err)
	}

	slog.Log(ctx, level, "SQL query", attrs...)
}

// ExecContext executes a query and logs the timing
func (l *loggingDB) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	start := time.Now()
	result, err := l.db.ExecContext(ctx, query, args...)
	l.logQuery(ctx, "Exec", query, start, err)
	return result, err
}

// PrepareContext prepares a statement and logs the timing
func (l *loggingDB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	start := time.Now()
	stmt, err := l.db.PrepareContext(ctx, query)
	l.logQuery(ctx, "Prepare", query, start, err)
	return stmt, err
}

// QueryContext executes a query and logs the timing
func (l *loggingDB) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	start := time.Now()
	rows, err := l.db.QueryContext(ctx, query, args...)
	l.logQuery(ctx, "Query", query, start, err)
	return rows, err
}

// QueryRowContext executes a query and logs the timing
func (l *loggingDB) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	start := time.Now()
	row := l.db.QueryRowContext(ctx, query, args...)
	l.logQuery(ctx, "QueryRow", query, start, nil)
	return row
}

// Ensure loggingDB implements DBTX interface
var _ db.DBTX = (*loggingDB)(nil)
