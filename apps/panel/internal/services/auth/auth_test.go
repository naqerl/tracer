package auth

import (
	"database/sql"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	paneldb "tracer/apps/panel/db"
	panelmigrations "tracer/apps/panel/sql/panel"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, panelmigrations.Embed)
	if err != nil {
		t.Fatalf("create goose provider: %v", err)
	}

	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	return db
}

func TestGenerateAndValidateCollectorAPIKey(t *testing.T) {
	ctx := t.Context()
	db := newTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	svc := New(db)

	key, row, err := svc.GenerateCollectorAPIKey(ctx, "prod-a")
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}
	if key == "" {
		t.Fatal("expected plaintext key")
	}
	if row == nil {
		t.Fatal("expected collector api key row")
	}
	if row.KeyHash == "" {
		t.Fatal("expected stored key hash")
	}
	if row.KeyHash == key {
		t.Fatal("expected stored key to be hashed")
	}

	valid, err := svc.ValidateCollectorAPIKey(ctx, key)
	if err != nil {
		t.Fatalf("validate collector api key: %v", err)
	}
	if !valid {
		t.Fatal("expected generated collector key to validate")
	}

	invalid, err := svc.ValidateCollectorAPIKey(ctx, "wrong-key")
	if err != nil {
		t.Fatalf("validate wrong collector api key: %v", err)
	}
	if invalid {
		t.Fatal("expected wrong collector key to fail validation")
	}
}

func TestListCollectorAPIKeysReturnsMaskedDisplay(t *testing.T) {
	ctx := t.Context()
	db := newTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	svc := New(db)

	key, _, err := svc.GenerateCollectorAPIKey(ctx, "prod-a")
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	display, err := svc.GetCollectorAPIKeyDisplay(ctx)
	if err != nil {
		t.Fatalf("get collector api key display: %v", err)
	}

	want := maskedAPIKeySuffix(key)
	if display != want {
		t.Fatalf("expected masked display %q, got %q", want, display)
	}
}

func TestValidateCollectorAPIKeyUpdatesLastUsedAt(t *testing.T) {
	ctx := t.Context()
	db := newTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	svc := New(db)

	key, row, err := svc.GenerateCollectorAPIKey(ctx, "prod-a")
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	valid, err := svc.ValidateCollectorAPIKey(ctx, key)
	if err != nil {
		t.Fatalf("validate collector api key: %v", err)
	}
	if !valid {
		t.Fatal("expected collector key to validate")
	}

	updated, err := paneldb.New(db).GetCollectorAPIKeyByName(ctx, row.Name)
	if err != nil {
		t.Fatalf("get collector api key by name: %v", err)
	}
	if !updated.LastUsedAt.Valid {
		t.Fatal("expected last_used_at to be updated")
	}
	if got := updated.LastUsedAt.Time.Location().String(); got != "UTC" {
		t.Fatalf("expected UTC timestamp, got %s (%s)", updated.LastUsedAt.Time.Format(time.RFC3339Nano), got)
	}
}
