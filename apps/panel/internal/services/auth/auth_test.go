package auth

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	_, err = db.Exec(`
CREATE TABLE config (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}

	return db
}

func TestSetAndValidateAPIKey_HashedStorage(t *testing.T) {
	ctx := t.Context()
	db := newTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	svc := New(db)

	const key = "test-api-key-123"
	if err := svc.SetAPIKey(ctx, key); err != nil {
		t.Fatalf("set api key: %v", err)
	}

	valid, err := svc.ValidateAPIKey(ctx, key)
	if err != nil {
		t.Fatalf("validate api key: %v", err)
	}
	if !valid {
		t.Fatalf("expected api key to validate")
	}

	invalid, err := svc.ValidateAPIKey(ctx, "wrong-key")
	if err != nil {
		t.Fatalf("validate wrong api key: %v", err)
	}
	if invalid {
		t.Fatalf("expected wrong key to fail validation")
	}
}

func TestGetAPIKey_ReturnsMaskedPreviewForHashedKey(t *testing.T) {
	ctx := t.Context()
	db := newTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	svc := New(db)

	const key = "abc123-secret"
	if err := svc.SetAPIKey(ctx, key); err != nil {
		t.Fatalf("set api key: %v", err)
	}

	value, err := svc.GetAPIKey(ctx)
	if err != nil {
		t.Fatalf("get api key: %v", err)
	}
	if len(value) == 0 || value[:4] != "****" {
		t.Fatalf("expected masked key, got %q", value)
	}
}
