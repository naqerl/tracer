package auth

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	paneldb "tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/testutil"
)

func TestGenerateAndValidateCollectorAPIKey(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	req := GenerateCollectorAPIKeyRequest{Name: "prod-a"}
	if err := req.Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}

	created, err := svc.GenerateCollectorAPIKey(ctx, domain.System{}, req)
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}
	if created.Plaintext == "" {
		t.Fatal("expected plaintext key")
	}
	if created.Key.ID == 0 || created.Key.Name != req.Name {
		t.Fatalf("expected created key with ID and name, got %+v", created.Key)
	}

	if !created.Key.CanReveal {
		t.Fatal("expected generated collector key to be revealable")
	}

	valid, err := svc.ValidateCollectorAPIKey(ctx, created.Plaintext)
	if err != nil {
		t.Fatalf("validate collector api key: %v", err)
	}
	if valid != 0 {
		t.Fatalf("expected user_id 0 for system-created key, got %d", valid)
	}

	invalid, err := svc.ValidateCollectorAPIKey(ctx, "wrong-key")
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for wrong key, got valid=%d err=%v", invalid, err)
	}
}

func TestListCollectorAPIKeysReturnsIDAndName(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	if _, err := svc.GenerateCollectorAPIKey(ctx, domain.System{}, GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	keys, err := svc.ListCollectorAPIKeys(ctx, domain.System{})
	if err != nil {
		t.Fatalf("list collector api keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("expected 1 collector api key, got %d", len(keys))
	}
	if keys[0].Name != "prod-a" || keys[0].ID == 0 {
		t.Fatalf("expected key with name prod-a and non-zero id, got %+v", keys[0])
	}
}

func TestValidateCollectorAPIKeyUpdatesLastUsedAt(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	req := GenerateCollectorAPIKeyRequest{Name: "prod-a"}
	if err := req.Validate(); err != nil {
		t.Fatalf("validate request: %v", err)
	}

	created, err := svc.GenerateCollectorAPIKey(ctx, domain.System{}, req)
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	valid, err := svc.ValidateCollectorAPIKey(ctx, created.Plaintext)
	if err != nil {
		t.Fatalf("validate collector api key: %v", err)
	}
	if valid != 0 {
		t.Fatalf("expected user_id 0 for system-created key, got %d", valid)
	}

	row := collectorAPIKeyRow(t, svc, req.Name)
	if !row.LastUsedSet {
		t.Fatal("expected last_used_at to be updated")
	}
	lastUsedTime := time.Unix(row.LastUsedAt, 0).UTC()
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
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

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
	if _, _, err := svc.ValidateSession(ctx, oldToken); !errors.Is(err, ErrSessionExpired) {
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

func TestResetPasswordSamePasswordKeepsSessions(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	user, err := svc.Register(ctx, RegisterRequest{
		Username: "admin",
		Password: "same-password",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	// Create a session before the reset; it must survive the no-op reset.
	token, err := svc.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Capture the stored hash before the reset to prove no write happens.
	before, err := paneldb.New(db.Read()).GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user by id: %v", err)
	}

	if err := svc.ResetPassword(ctx, "admin", "same-password"); err != nil {
		t.Fatalf("reset password with unchanged value: %v", err)
	}

	// The stored hash must be untouched: the reset is a true no-op write-wise.
	after, err := paneldb.New(db.Read()).GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user by id: %v", err)
	}
	if before.PasswordHash != after.PasswordHash {
		t.Fatal("expected no-op reset to leave the password hash untouched")
	}

	// Pre-reset session token must still be valid.
	if _, _, err := svc.ValidateSession(ctx, token); err != nil {
		t.Fatalf("expected session to survive no-op reset, got %v", err)
	}

	// The unchanged password must still log in.
	if _, err := svc.Login(ctx, "admin", "same-password"); err != nil {
		t.Fatalf("login with unchanged password after no-op reset: %v", err)
	}
}

func TestResetPasswordSamePasswordPromotesToAdmin(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	// Register as a regular user: the no-op reset must still promote them to
	// admin, preserving the reset contract without purging sessions.
	user, err := svc.Register(ctx, RegisterRequest{
		Username: "admin",
		Password: "same-password",
		IsAdmin:  false,
	})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	token, err := svc.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := svc.ResetPassword(ctx, "admin", "same-password"); err != nil {
		t.Fatalf("reset password with unchanged value: %v", err)
	}

	// Sessions survive the no-op reset...
	if _, _, err := svc.ValidateSession(ctx, token); err != nil {
		t.Fatalf("expected session to survive no-op reset, got %v", err)
	}

	// ...and the user is promoted to admin.
	row, err := paneldb.New(db.Read()).GetUserByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("get user by id: %v", err)
	}
	if row.IsAdmin != 1 {
		t.Fatalf("expected user to be admin after no-op reset, got is_admin=%d", row.IsAdmin)
	}
}

func TestResetPasswordRepairsCorruptHash(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	// A corrupt stored hash must not short-circuit the reset into a no-op:
	// the check fails (not with ErrInvalidCredentials), the reset proceeds,
	// and the re-hash repairs the stored hash.
	err := db.Write(ctx, func(tx paneldb.DBTX) error {
		_, err := paneldb.New(tx).CreateUser(ctx, paneldb.CreateUserParams{
			Username:     "admin",
			PasswordHash: "not-a-valid-bcrypt-hash",
			IsAdmin:      1,
			CreatedAt:    time.Now().UTC().Unix(),
		})
		return err
	})
	if err != nil {
		t.Fatalf("create user with corrupt hash: %v", err)
	}

	if err := svc.ResetPassword(ctx, "admin", "new-password"); err != nil {
		t.Fatalf("reset password over corrupt hash: %v", err)
	}

	// The new password must work, proving the hash was repaired.
	if _, err := svc.Login(ctx, "admin", "new-password"); err != nil {
		t.Fatalf("login with new password after repair: %v", err)
	}
}

func TestResetPasswordUserNotFound(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	err := svc.ResetPassword(ctx, "ghost", "new-password")
	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestResetPasswordRejectsInvalidPassword(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

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

// backdateSession rewrites a session's expires_at, the way a test-only
// helper must: no public service method can create a session with an
// arbitrary deadline.
func backdateSession(t *testing.T, db *dbmanager.DBHandle, token string, expiresAt int64) {
	t.Helper()
	if err := db.Write(t.Context(), func(tx paneldb.DBTX) error {
		return paneldb.New(tx).RefreshSession(t.Context(), paneldb.RefreshSessionParams{
			ExpiresAt: expiresAt,
			ID:        token,
		})
	}); err != nil {
		t.Fatalf("backdate session: %v", err)
	}
}

func sessionExpiry(t *testing.T, db *dbmanager.DBHandle, token string) int64 {
	t.Helper()
	session, err := paneldb.New(db.Read()).GetSession(t.Context(), token)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	return session.ExpiresAt
}

func TestValidateSessionNoRefreshWithinHalfLife(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)
	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	user, err := svc.Register(ctx, RegisterRequest{Username: "admin", Password: "password", IsAdmin: true})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token, err := svc.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// A session that has used less than half of its lifetime must validate
	// without a refresh. A deadline with 22h remaining is inside the first
	// half of its 24h lifetime.
	backdateSession(t, db, token, time.Now().Add(SessionDuration-2*time.Hour).Unix())
	before := sessionExpiry(t, db, token)

	userID, refresh, err := svc.ValidateSession(ctx, token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if userID != user.ID {
		t.Fatalf("expected user %d, got %d", user.ID, userID)
	}
	if refresh {
		t.Fatal("expected no refresh within the first half of the session lifetime")
	}
	if got := sessionExpiry(t, db, token); got != before {
		t.Fatalf("expected expires_at untouched, got %d want %d", got, before)
	}
}

func TestValidateSessionRefreshesWhenHalfExpired(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)
	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	user, err := svc.Register(ctx, RegisterRequest{Username: "admin", Password: "password", IsAdmin: true})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token, err := svc.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// More than half of the lifetime has elapsed: the deadline must be
	// extended by SessionDuration and refresh reported. A deadline with 11h
	// remaining is past the halfway point of its 24h lifetime but not yet
	// expired.
	backdateSession(t, db, token, time.Now().Add(SessionDuration/2-time.Hour).Unix())

	userID, refresh, err := svc.ValidateSession(ctx, token)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if userID != user.ID {
		t.Fatalf("expected user %d, got %d", user.ID, userID)
	}
	if !refresh {
		t.Fatal("expected refresh once half the session lifetime has elapsed")
	}
	now := time.Now().Unix()
	if got := sessionExpiry(t, db, token); got < now+int64(SessionDuration.Seconds()) {
		t.Fatalf("expected expires_at extended to at least %d, got %d", now+int64(SessionDuration.Seconds()), got)
	}
}

func TestValidateSessionDeletesExpired(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)
	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	user, err := svc.Register(ctx, RegisterRequest{Username: "admin", Password: "password", IsAdmin: true})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token, err := svc.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	backdateSession(t, db, token, time.Now().Add(-time.Second).Unix())

	if _, _, err := svc.ValidateSession(ctx, token); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("expected ErrSessionExpired, got %v", err)
	}
	if _, err := paneldb.New(db.Read()).GetSession(ctx, token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected expired session row deleted, got %v", err)
	}
}

func TestRevealCollectorAPIKeyReturnsPlaintext(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	created, err := svc.GenerateCollectorAPIKey(ctx, domain.System{}, GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}
	row := collectorAPIKeyRow(t, svc, "prod-a")
	if !row.CanReveal {
		t.Fatal("expected stored ciphertext for revealable key")
	}

	revealed, err := svc.RevealCollectorAPIKey(ctx, domain.User{ID: 1, Role: domain.RoleAdmin}, row.ID)
	if err != nil {
		t.Fatalf("reveal collector api key: %v", err)
	}
	if revealed != created.Plaintext {
		t.Fatalf("expected revealed key to match plaintext, got %q want %q", revealed, created.Plaintext)
	}
}

func TestGetCollectorAPIKeyUnknownIDReturnsNoRows(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	// Unknown ids surface as sql.ErrNoRows from the getter, which the handler
	// maps to 404.
	_, err := svc.GetCollectorAPIKeyByID(ctx, domain.System{}, 999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestRevealCollectorAPIKeyLegacyKeyWithoutCiphertext(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	// Simulate a key created before reveal support: store only the hash.
	err := db.Write(ctx, func(tx paneldb.DBTX) error {
		_, err := paneldb.New(tx).CreateCollectorAPIKey(ctx, paneldb.CreateCollectorAPIKeyParams{
			Name:      "legacy",
			KeyHash:   "deadbeef",
			CreatedAt: 1,
		})
		return err
	})
	if err != nil {
		t.Fatalf("insert legacy key: %v", err)
	}

	row, err := svc.GetCollectorAPIKeyByID(ctx, domain.System{}, 1)
	if err != nil {
		t.Fatalf("get legacy key: %v", err)
	}

	_, err = svc.RevealCollectorAPIKey(ctx, domain.User{ID: 1, Role: domain.RoleAdmin}, row.ID)
	if !errors.Is(err, ErrKeyNotRevealable) {
		t.Fatalf("expected ErrKeyNotRevealable, got %v", err)
	}
}

func TestRevealCollectorAPIKeyFailsWithWrongMasterKey(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	if _, err := svc.GenerateCollectorAPIKey(ctx, domain.System{}, GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}
	row := collectorAPIKeyRow(t, svc, "prod-a")

	// A service constructed with a different master key cannot reveal.
	other := New(db, []byte("fedcba9876543210fedcba9876543210"), bcrypt.MinCost)
	_, err := other.RevealCollectorAPIKey(ctx, domain.User{ID: 1, Role: domain.RoleAdmin}, row.ID)
	if !errors.Is(err, ErrKeyDecryptFailed) {
		t.Fatalf("expected ErrKeyDecryptFailed with wrong master key, got %v", err)
	}
}

func TestRevealCollectorAPIKeyDeniedForNonAdmin(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	if _, err := svc.GenerateCollectorAPIKey(ctx, domain.System{}, GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}
	row := collectorAPIKeyRow(t, svc, "prod-a")

	_, err := svc.RevealCollectorAPIKey(ctx, domain.User{ID: 1, Role: domain.RoleUser}, row.ID)
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for non-admin, got %v", err)
	}
}

func TestRevealCollectorAPIKeyAllowedForSystem(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	created, err := svc.GenerateCollectorAPIKey(ctx, domain.System{}, GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}
	row := collectorAPIKeyRow(t, svc, "prod-a")

	revealed, err := svc.RevealCollectorAPIKey(ctx, domain.System{}, row.ID)
	if err != nil {
		t.Fatalf("reveal as system: %v", err)
	}
	if revealed != created.Plaintext {
		t.Fatalf("expected revealed key to match plaintext, got %q want %q", revealed, created.Plaintext)
	}
}

func TestRevealUserAPIKeyReturnsPlaintextForOwner(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	created, err := svc.GenerateUserAPIKey(ctx, domain.User{ID: 7, Role: domain.RoleUser}, GenerateUserAPIKeyRequest{UserID: 7, Name: "my-key"})
	if err != nil {
		t.Fatalf("generate user api key: %v", err)
	}
	row := userAPIKeyRow(t, svc, 7, "my-key")

	revealed, err := svc.RevealUserAPIKey(ctx, domain.User{ID: 7, Role: domain.RoleUser}, row.ID)
	if err != nil {
		t.Fatalf("reveal user api key: %v", err)
	}
	if revealed != created.Plaintext {
		t.Fatalf("expected revealed key to match plaintext, got %q want %q", revealed, created.Plaintext)
	}
}

func TestRevealUserAPIKeyAllowedForAdmin(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	created, err := svc.GenerateUserAPIKey(ctx, domain.User{ID: 7, Role: domain.RoleUser}, GenerateUserAPIKeyRequest{UserID: 7, Name: "my-key"})
	if err != nil {
		t.Fatalf("generate user api key: %v", err)
	}
	row := userAPIKeyRow(t, svc, 7, "my-key")

	revealed, err := svc.RevealUserAPIKey(ctx, domain.User{ID: 99, Role: domain.RoleAdmin}, row.ID)
	if err != nil {
		t.Fatalf("reveal as admin: %v", err)
	}
	if revealed != created.Plaintext {
		t.Fatalf("expected revealed key to match plaintext, got %q want %q", revealed, created.Plaintext)
	}
}

func TestRevealUserAPIKeyDeniedForNonOwner(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	if _, err := svc.GenerateUserAPIKey(ctx, domain.User{ID: 7, Role: domain.RoleUser}, GenerateUserAPIKeyRequest{UserID: 7, Name: "my-key"}); err != nil {
		t.Fatalf("generate user api key: %v", err)
	}
	row := userAPIKeyRow(t, svc, 7, "my-key")

	_, err := svc.RevealUserAPIKey(ctx, domain.User{ID: 99, Role: domain.RoleUser}, row.ID)
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for non-owner, got %v", err)
	}
}

func TestGenerateUserAPIKeyDeniedForNonOwner(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	_, err := svc.GenerateUserAPIKey(ctx, domain.User{ID: 99, Role: domain.RoleUser}, GenerateUserAPIKeyRequest{UserID: 7, Name: "my-key"})
	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for non-owner generate, got %v", err)
	}
}

func TestUserAPIKeyAccessDeniedForUnauthenticated(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	if _, err := svc.GenerateUserAPIKey(ctx, domain.User{ID: 7, Role: domain.RoleUser}, GenerateUserAPIKeyRequest{UserID: 7, Name: "my-key"}); err != nil {
		t.Fatalf("generate user api key: %v", err)
	}
	row := userAPIKeyRow(t, svc, 7, "my-key")

	// Unauthenticated callers must be denied everywhere.
	if _, err := svc.RevealUserAPIKey(ctx, domain.Unauthenticated{}, row.ID); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for unauthenticated reveal, got %v", err)
	}
	if _, err := svc.GetUserAPIKeyByID(ctx, domain.Unauthenticated{}, row.ID); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for unauthenticated get, got %v", err)
	}
	if _, err := svc.ListUserAPIKeys(ctx, domain.Unauthenticated{}, 7); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for unauthenticated list, got %v", err)
	}
	if err := svc.DeleteUserAPIKey(ctx, domain.Unauthenticated{}, row.ID); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for unauthenticated delete, got %v", err)
	}
}

func TestCollectorAPIKeyAccessDeniedForUnauthenticated(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	if _, err := svc.GenerateCollectorAPIKey(ctx, domain.System{}, GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}
	row := collectorAPIKeyRow(t, svc, "prod-a")

	if _, err := svc.RevealCollectorAPIKey(ctx, domain.Unauthenticated{}, row.ID); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for unauthenticated reveal, got %v", err)
	}
}

func TestCollectorAPIKeyManagementDeniedForNonAdmin(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	nonAdmin := domain.User{ID: 1, Role: domain.RoleUser}

	if _, err := svc.GenerateCollectorAPIKey(ctx, nonAdmin, GenerateCollectorAPIKeyRequest{Name: "prod-a"}); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for non-admin generate, got %v", err)
	}
	if _, err := svc.ListCollectorAPIKeys(ctx, nonAdmin); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for non-admin list, got %v", err)
	}
	if err := svc.DeleteCollectorAPIKey(ctx, nonAdmin, 1); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for non-admin delete, got %v", err)
	}
}

func TestCollectorAPIKeyManagementAllowedForAdmin(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	svc := New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	admin := domain.User{ID: 1, Role: domain.RoleAdmin}

	if _, err := svc.GenerateCollectorAPIKey(ctx, admin, GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
		t.Fatalf("generate as admin: %v", err)
	}
	if _, err := svc.ListCollectorAPIKeys(ctx, admin); err != nil {
		t.Fatalf("list as admin: %v", err)
	}
	row := collectorAPIKeyRow(t, svc, "prod-a")
	if err := svc.DeleteCollectorAPIKey(ctx, admin, row.ID); err != nil {
		t.Fatalf("delete as admin: %v", err)
	}
}

// userAPIKeyRow resolves the full user API key row (including ciphertext)
// through the public read APIs after GenerateUserAPIKey returns only the
// plaintext.
func userAPIKeyRow(t *testing.T, svc *Service, userID int64, name string) UserAPIKey {
	t.Helper()
	actor := domain.User{ID: userID, Role: domain.RoleUser}
	keys, err := svc.ListUserAPIKeys(t.Context(), actor, userID)
	if err != nil {
		t.Fatalf("list user api keys: %v", err)
	}
	var id int64
	for _, k := range keys {
		if k.Name == name {
			id = k.ID
			break
		}
	}
	if id == 0 {
		t.Fatalf("user api key %q not found", name)
	}
	row, err := svc.GetUserAPIKeyByID(t.Context(), actor, id)
	if err != nil {
		t.Fatalf("get user api key: %v", err)
	}
	return row
}

// collectorAPIKeyRow resolves the full collector API key row (including
// ciphertext) through the public read APIs after GenerateCollectorAPIKey
// returns only the plaintext.
func collectorAPIKeyRow(t *testing.T, svc *Service, name string) CollectorAPIKey {
	t.Helper()
	keys, err := svc.ListCollectorAPIKeys(t.Context(), domain.System{})
	if err != nil {
		t.Fatalf("list collector api keys: %v", err)
	}
	var id int64
	for _, k := range keys {
		if k.Name == name {
			id = k.ID
			break
		}
	}
	if id == 0 {
		t.Fatalf("collector api key %q not found", name)
	}
	row, err := svc.GetCollectorAPIKeyByID(t.Context(), domain.System{}, id)
	if err != nil {
		t.Fatalf("get collector api key: %v", err)
	}
	return row
}
