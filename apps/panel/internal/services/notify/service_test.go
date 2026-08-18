package notify

import (
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	authsvc "tracer/apps/panel/internal/services/auth"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/testutil"

	"tracer/apps/panel/internal/domain"
)

// TestGetSubscriptionOptions covers the collector/app candidates returned for
// a notifier: admins see every pair with the subscribed ones flagged, and the
// options respect the owner's access policy for non-admins.
func TestGetSubscriptionOptions(t *testing.T) {
	db := testutil.NewTestDB(t)
	userSvc := usersvc.NewService(db)
	authSvc := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	admin, err := authSvc.Register(t.Context(), authsvc.RegisterRequest{Username: "admin", Password: "secret", IsAdmin: true})
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}

	mgr := testutil.NewTestDBManager(t)
	if _, err := mgr.CreateAppDB("prod", "api"); err != nil {
		t.Fatalf("create prod/api: %v", err)
	}
	if _, err := mgr.CreateAppDB("prod", "worker"); err != nil {
		t.Fatalf("create prod/worker: %v", err)
	}
	if _, err := mgr.CreateAppDB("staging", "web"); err != nil {
		t.Fatalf("create staging/web: %v", err)
	}

	collected := &fakeNotifier{}
	service := New(db, userSvc, mgr, map[string]FromRawJSON{
		"test": func(_, _ string) (Notifier, error) { return collected, nil },
	})
	id, err := service.Add(t.Context(), domain.User{ID: admin.ID, Role: domain.RoleAdmin}, AddNotifierRequest{
		UserID: admin.ID,
		Name:   "issues",
		Type:   "test",
		Config: "{}",
	})
	if err != nil {
		t.Fatalf("add notifier: %v", err)
	}
	if err := service.SetSubscriptions(t.Context(), domain.User{ID: admin.ID, Role: domain.RoleAdmin}, id, []Subscription{
		{CollectorName: "prod", AppName: "api"},
	}); err != nil {
		t.Fatalf("set subscriptions: %v", err)
	}

	options, err := service.GetSubscriptionOptions(t.Context(), domain.User{ID: admin.ID, Role: domain.RoleAdmin}, id)
	if err != nil {
		t.Fatalf("get subscription options: %v", err)
	}

	want := map[string]bool{"prod:api": true, "prod:worker": false, "staging:web": false}
	if len(options) != len(want) {
		t.Fatalf("expected %d options, got %d: %+v", len(want), len(options), options)
	}
	for _, opt := range options {
		key := opt.Collector + ":" + opt.App
		enabled, ok := want[key]
		if !ok {
			t.Errorf("unexpected option %q", key)
			continue
		}
		if opt.Granted != enabled {
			t.Errorf("option %q granted = %v, want %v", key, opt.Granted, enabled)
		}
	}
}

// TestGetSubscriptionOptionsFiltersByAccess verifies a non-admin owner only
// sees the collector/app pairs they have access to.
func TestGetSubscriptionOptionsFiltersByAccess(t *testing.T) {
	db := testutil.NewTestDB(t)
	userSvc := usersvc.NewService(db)
	authSvc := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	user, err := authSvc.Register(t.Context(), authsvc.RegisterRequest{Username: "alice", Password: "secret", IsAdmin: false})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}
	if err := userSvc.GrantAccess(t.Context(), user.ID, usersvc.AppAccess{CollectorName: "prod", AppName: "api"}); err != nil {
		t.Fatalf("grant access: %v", err)
	}

	mgr := testutil.NewTestDBManager(t)
	if _, err := mgr.CreateAppDB("prod", "api"); err != nil {
		t.Fatalf("create prod/api: %v", err)
	}
	if _, err := mgr.CreateAppDB("prod", "worker"); err != nil {
		t.Fatalf("create prod/worker: %v", err)
	}

	collected := &fakeNotifier{}
	service := New(db, userSvc, mgr, map[string]FromRawJSON{
		"test": func(_, _ string) (Notifier, error) { return collected, nil },
	})
	id, err := service.Add(t.Context(), domain.User{ID: user.ID, Role: domain.RoleUser}, AddNotifierRequest{
		UserID: user.ID,
		Name:   "issues",
		Type:   "test",
		Config: "{}",
	})
	if err != nil {
		t.Fatalf("add notifier: %v", err)
	}

	options, err := service.GetSubscriptionOptions(t.Context(), domain.User{ID: user.ID, Role: domain.RoleUser}, id)
	if err != nil {
		t.Fatalf("get subscription options: %v", err)
	}
	if len(options) != 1 || options[0].Collector != "prod" || options[0].App != "api" {
		t.Fatalf("expected only prod/api, got %+v", options)
	}

	// A non-owner cannot enumerate options for someone else's notifier.
	if _, err := service.GetSubscriptionOptions(t.Context(), domain.User{ID: 999, Role: domain.RoleUser}, id); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for non-owner, got %v", err)
	}
}

// TestListReturnsServiceTypes covers notify.Service.List returning service
// types (Setup) rather than db rows: admins see all notifiers, non-admins see
// only their own.
func TestListReturnsServiceTypes(t *testing.T) {
	db := testutil.NewTestDB(t)
	userSvc := usersvc.NewService(db)
	authSvc := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	admin, err := authSvc.Register(t.Context(), authsvc.RegisterRequest{Username: "admin", Password: "secret", IsAdmin: true})
	if err != nil {
		t.Fatalf("register admin: %v", err)
	}
	user, err := authSvc.Register(t.Context(), authsvc.RegisterRequest{Username: "bob", Password: "secret", IsAdmin: false})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	collected := &fakeNotifier{}
	service := New(db, userSvc, testutil.NewTestDBManager(t), map[string]FromRawJSON{
		"test": func(_, _ string) (Notifier, error) { return collected, nil },
	})

	adminActor := domain.User{ID: admin.ID, Role: domain.RoleAdmin}
	userActor := domain.User{ID: user.ID, Role: domain.RoleUser}

	// Admin creates a notifier for themselves and one for the user.
	for _, req := range []AddNotifierRequest{
		{UserID: admin.ID, Name: "admin-alerts", Type: "test", Config: "{}"},
		{UserID: user.ID, Name: "user-alerts", Type: "test", Config: "{}"},
	} {
		actor := adminActor
		if req.UserID == user.ID {
			actor = userActor
		}
		if _, err := service.Add(t.Context(), actor, req); err != nil {
			t.Fatalf("add notifier %s: %v", req.Name, err)
		}
	}

	// Admin sees both notifiers as Setup values with all fields.
	all, err := service.List(t.Context(), adminActor)
	if err != nil {
		t.Fatalf("list as admin: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 notifiers for admin, got %d: %+v", len(all), all)
	}
	for _, s := range all {
		if s.Name == "" || s.Type != "test" || s.UserID == 0 || s.Enabled != 1 {
			t.Errorf("expected full Setup, got %+v", s)
		}
	}

	// Non-admin sees only their own notifier.
	own, err := service.List(t.Context(), userActor)
	if err != nil {
		t.Fatalf("list as user: %v", err)
	}
	if len(own) != 1 || own[0].Name != "user-alerts" {
		t.Fatalf("expected only user-alerts for non-admin, got %+v", own)
	}
}
