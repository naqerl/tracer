package auth

import (
	"database/sql"
	"errors"
	"fmt"
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

	// Unique in-memory database per test so parallel or repeated runs
	// cannot share state through sqlite's shared cache.
	name := strings.ReplaceAll(t.Name(), "/", "_")
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared", name))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close db: %v", err)
		}
	})

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

	updated, err := paneldb.New(db.Read()).GetCollectorAPIKeyByName(ctx, row.Name)
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

func TestResetPasswordOverwritesPasswordAndInvalidatesSessions(t *testing.T) {
	ctx := t.Context()
	db := newTestDB(t)

	svc := New(db)

	// Register as a regular user: the reset must promote them to admin.
	user, err := svc.Register(ctx, RegisterRequest{
		Username: "admin",
		Password: "old-password",
		IsAdmin:  false,
	})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	// Create a session before the reset; it must be invalidated.
	oldToken, err := svc.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := svc.ResetPassword(ctx, "admin", "new-password"); err != nil {
		t.Fatalf("reset password: %v", err)
	}

	// Old password must no longer work.
	if _, err := svc.Login(ctx, "admin", "old-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials with old password, got %v", err)
	}

	// New password must work.
	if _, err := svc.Login(ctx, "admin", "new-password"); err != nil {
		t.Fatalf("login with new password: %v", err)
	}

	// Pre-reset session token must be invalidated.
	if _, err := svc.ValidateSession(ctx, oldToken); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired for pre-reset session, got %v", err)
	}

	// User must be promoted to admin.
	row, err := paneldb.New(db.Read()).GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user by id: %v", err)
	}
	if row.IsAdmin != 1 {
		t.Fatalf("expected user to be admin after reset, got is_admin=%d", row.IsAdmin)
	}
}

func TestResetPasswordUserNotFound(t *testing.T) {
	ctx := t.Context()
	db := newTestDB(t)

	svc := New(db)

	err := svc.ResetPassword(ctx, "ghost", "new-password")
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestResetPasswordRejectsInvalidPassword(t *testing.T) {
	ctx := t.Context()
	db := newTestDB(t)

	svc := New(db)

	if _, err := svc.Register(ctx, RegisterRequest{
		Username: "admin",
		Password: "old-password",
		IsAdmin:  true,
	}); err != nil {
		t.Fatalf("register user: %v", err)
	}

	err := svc.ResetPassword(ctx, "admin", "short")
	if err == nil {
		t.Fatal("expected validation error for too-short password")
	}

	// Original password must be untouched.
	if _, err := svc.Login(ctx, "admin", "old-password"); err != nil {
		t.Fatalf("login with original password after rejected reset: %v", err)
	}
}
