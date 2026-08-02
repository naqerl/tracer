package cli

import (
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"

	paneldb "tracer/apps/panel/db"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/testutil"
)

func TestInitWithHandleCreatesAdminAndAPIKey(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)
	authSvc := auth.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	result, err := initWithHandle(ctx, authSvc, "admin", "password123", false, false)
	if err != nil {
		t.Fatalf("init panel: %v", err)
	}
	if result.AdminUsername != "admin" {
		t.Fatalf("expected admin username %q, got %q", "admin", result.AdminUsername)
	}
	if result.CollectorAPIKey == "" {
		t.Fatal("expected collector api key on fresh init")
	}

	if _, err := authSvc.Login(ctx, "admin", "password123"); err != nil {
		t.Fatalf("login with created admin user: %v", err)
	}
}

func TestInitWithHandleAlreadyInitializedWithoutForce(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	authSvc := auth.New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	if _, err := authSvc.Register(ctx, auth.RegisterRequest{
		Username: "admin",
		Password: "password123",
		IsAdmin:  true,
	}); err != nil {
		t.Fatalf("register user: %v", err)
	}

	_, err := initWithHandle(ctx, authSvc, "admin", "password123", false, false)
	if !errors.Is(err, ErrAlreadyInitialized) {
		t.Fatalf("expected ErrAlreadyInitialized, got %v", err)
	}
}

func TestInitWithHandleForceResetsPassword(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	authSvc := auth.New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	// Register as a regular user: the force reset must promote them to admin.
	if _, err := authSvc.Register(ctx, auth.RegisterRequest{
		Username: "admin",
		Password: "old-password",
		IsAdmin:  false,
	}); err != nil {
		t.Fatalf("register user: %v", err)
	}

	result, err := initWithHandle(ctx, authSvc, "admin", "new-password", false, true)
	if err != nil {
		t.Fatalf("force init panel: %v", err)
	}
	if result.AdminPassword != "new-password" {
		t.Fatalf("expected reset password in result, got %q", result.AdminPassword)
	}
	if result.CollectorAPIKey != "" {
		t.Fatalf("expected no new collector api key on force reset, got %q", result.CollectorAPIKey)
	}

	// Old password must no longer work; new password must.
	if _, err := authSvc.Login(ctx, "admin", "old-password"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials with old password, got %v", err)
	}
	if _, err := authSvc.Login(ctx, "admin", "new-password"); err != nil {
		t.Fatalf("login with reset password: %v", err)
	}

	// The reset must promote the user to admin.
	row, err := paneldb.New(db.Read()).FindUserCredsByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("find admin user: %v", err)
	}
	userRow, err := paneldb.New(db.Read()).GetUserByID(ctx, row.ID)
	if err != nil {
		t.Fatalf("get admin user: %v", err)
	}
	if userRow.IsAdmin != 1 {
		t.Fatalf("expected reset user to be promoted to admin, got is_admin=%d", userRow.IsAdmin)
	}
}

func TestInitWithHandleForceCreatesMissingAdmin(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)

	// Panel already initialized with a different user.
	authSvc := auth.New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	if _, err := authSvc.Register(ctx, auth.RegisterRequest{
		Username: "someone",
		Password: "password123",
		IsAdmin:  false,
	}); err != nil {
		t.Fatalf("register user: %v", err)
	}

	result, err := initWithHandle(ctx, authSvc, "admin", "password123", false, true)
	if err != nil {
		t.Fatalf("force init panel: %v", err)
	}
	if result.AdminUsername != "admin" {
		t.Fatalf("expected admin username %q, got %q", "admin", result.AdminUsername)
	}

	// The missing admin user must have been created and be able to log in.
	if _, err := authSvc.Login(ctx, "admin", "password123"); err != nil {
		t.Fatalf("login with newly created admin: %v", err)
	}

	// It must be an admin.
	row, err := paneldb.New(db.Read()).FindUserCredsByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("find admin user: %v", err)
	}
	userRow, err := paneldb.New(db.Read()).GetUserByID(ctx, row.ID)
	if err != nil {
		t.Fatalf("get admin user: %v", err)
	}
	if userRow.IsAdmin != 1 {
		t.Fatalf("expected newly created user to be admin, got is_admin=%d", userRow.IsAdmin)
	}
}
