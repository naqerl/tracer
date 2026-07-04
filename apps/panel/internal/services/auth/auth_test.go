package auth

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	paneldb "tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	panelmigrations "tracer/apps/panel/sql/panel"
)

func newTestDB(t *testing.T) *dbmanager.DBHandle {
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

	return dbmanager.NewDBHandle(db, "test-panel")
}

func TestGenerateAndValidateCollectorAPIKey(t *testing.T) {
	ctx := t.Context()
	db := newTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	svc := New(db)

	req := GenerateCollectorAPIKeyRequest{Name: "prod-a"}
	if err := req.Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}

	key, row, err := svc.GenerateCollectorAPIKey(ctx, req)
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

	req := GenerateCollectorAPIKeyRequest{Name: "prod-a"}
	if err := req.Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}

	key, _, err := svc.GenerateCollectorAPIKey(ctx, req)
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

	req := GenerateCollectorAPIKeyRequest{Name: "prod-a"}
	if err := req.Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}

	key, row, err := svc.GenerateCollectorAPIKey(ctx, req)
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

	updated, err := paneldb.New(db.Read(ctx)).GetCollectorAPIKeyByName(ctx, row.Name)
	if err != nil {
		t.Fatalf("get collector api key by name: %v", err)
	}
	if !updated.LastUsedAt.Valid {
		t.Fatal("expected last_used_at to be updated")
	}
	lastUsedTime := time.Unix(updated.LastUsedAt.Int64, 0).UTC()
	if got := lastUsedTime.Location().String(); got != "UTC" {
		t.Fatalf("expected UTC timestamp, got %s (%s)", lastUsedTime.Format(time.RFC3339Nano), got)
	}
}

func TestRegisterRejectsTooLongPassword(t *testing.T) {
	req := RegisterRequest{
		Username: "admin",
		Password: strings.Repeat("x", 73),
		IsAdmin:  true,
	}
	err := req.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestRegisterRejectsUsernameWithSurroundingWhitespace(t *testing.T) {
	req := RegisterRequest{
		Username: " admin ",
		Password: "secret",
		IsAdmin:  true,
	}
	err := req.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
}

func TestGenerateCollectorAPIKeyRejectsTooLongName(t *testing.T) {
	req := GenerateCollectorAPIKeyRequest{
		Name: strings.Repeat("n", 65),
	}
	err := req.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}
}
