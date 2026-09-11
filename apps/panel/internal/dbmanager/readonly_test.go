package dbmanager

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestReadOnlyDB_RejectsExec(t *testing.T) {
	f := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+f+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ro := &readOnlyDB{pool: db}
	_, err = ro.ExecContext(context.Background(), "CREATE TABLE t (x int)")
	if err != ErrReadOnly {
		t.Fatalf("expected ErrReadOnly, got %v", err)
	}
}

func TestReadOnlyDB_AllowsQuery(t *testing.T) {
	f := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+f+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Create table first using raw db
	_, err = db.ExecContext(context.Background(), "CREATE TABLE t (x int)")
	if err != nil {
		t.Fatal(err)
	}

	ro := &readOnlyDB{pool: db}
	rows, err := ro.QueryContext(context.Background(), "SELECT * FROM t")
	if err != nil {
		t.Fatalf("expected QueryContext to succeed, got %v", err)
	}
	rows.Close()
}

func TestReadOnlyDB_AllowsQueryRow(t *testing.T) {
	f := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+f+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ro := &readOnlyDB{pool: db}
	err = ro.QueryRowContext(context.Background(), "SELECT 1").Err()
	if err != nil {
		t.Fatalf("expected QueryRowContext to succeed, got %v", err)
	}
}

func TestReadOnlyDB_AllowsPrepare(t *testing.T) {
	f := t.TempDir() + "/test.db"
	db, err := sql.Open("sqlite", "file:"+f+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ro := &readOnlyDB{pool: db}
	stmt, err := ro.PrepareContext(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatalf("expected PrepareContext to succeed, got %v", err)
	}
	stmt.Close()
}
