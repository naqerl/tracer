package user

import (
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/testutil"
)

func TestFindByIDAllowsSelfAndAdmin(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)
	svc := NewService(db)
	authSvc := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	admin, err := authSvc.Register(ctx, authsvc.RegisterRequest{Username: "admin", Password: "secret", IsAdmin: true})
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	user, err := authSvc.Register(ctx, authsvc.RegisterRequest{Username: "alice", Password: "secret", IsAdmin: false})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	// Self lookup is allowed.
	got, err := svc.FindByID(ctx, domain.User{ID: user.ID, Role: domain.RoleUser}, user.ID)
	if err != nil {
		t.Fatalf("self lookup: %v", err)
	}
	if got.Username != "alice" {
		t.Fatalf("expected username alice, got %q", got.Username)
	}

	// Admin may look up anyone.
	if _, err := svc.FindByID(ctx, domain.User{ID: admin.ID, Role: domain.RoleAdmin}, user.ID); err != nil {
		t.Fatalf("admin lookup: %v", err)
	}

	// System may look up anyone.
	if _, err := svc.FindByID(ctx, domain.System{}, user.ID); err != nil {
		t.Fatalf("system lookup: %v", err)
	}

	// A non-admin may not look up someone else.
	if _, err := svc.FindByID(ctx, domain.User{ID: user.ID, Role: domain.RoleUser}, admin.ID); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for cross-user lookup, got %v", err)
	}

	// Unauthenticated is denied.
	if _, err := svc.FindByID(ctx, domain.Unauthenticated{}, user.ID); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for unauthenticated lookup, got %v", err)
	}
}

func TestListFiltersNonAdminToSelfAndAdmins(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)
	svc := NewService(db)
	authSvc := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	admin, err := authSvc.Register(ctx, authsvc.RegisterRequest{Username: "admin", Password: "secret", IsAdmin: true})
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	alice, err := authSvc.Register(ctx, authsvc.RegisterRequest{Username: "alice", Password: "secret", IsAdmin: false})
	if err != nil {
		t.Fatalf("register alice: %v", err)
	}
	if _, err := authSvc.Register(ctx, authsvc.RegisterRequest{Username: "bob", Password: "secret", IsAdmin: false}); err != nil {
		t.Fatalf("register bob: %v", err)
	}

	// Admin sees everyone.
	users, err := svc.List(ctx, domain.User{ID: admin.ID, Role: domain.RoleAdmin})
	if err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("expected 3 users for admin, got %d", len(users))
	}

	// Non-admin sees only themselves and the admin.
	users, err = svc.List(ctx, domain.User{ID: alice.ID, Role: domain.RoleUser})
	if err != nil {
		t.Fatalf("user list: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("expected 2 users for alice (self + admin), got %d", len(users))
	}
	for _, u := range users {
		if u.ID != alice.ID && u.Role != domain.RoleAdmin {
			t.Fatalf("unexpected user %d in non-admin list", u.ID)
		}
	}

	// System sees everyone.
	users, err = svc.List(ctx, domain.System{})
	if err != nil {
		t.Fatalf("system list: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("expected 3 users for system, got %d", len(users))
	}

	// Unauthenticated is denied.
	if _, err := svc.List(ctx, domain.Unauthenticated{}); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for unauthenticated list, got %v", err)
	}
}
