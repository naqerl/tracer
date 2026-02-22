package permissions

import (
	"testing"

	"github.com/ai-shift/tracer/apps/panel/db"
)

func TestIsAdmin(t *testing.T) {
	svc := &Service{}

	if svc.IsAdmin(nil) {
		t.Fatalf("nil user must not be admin")
	}

	admin := &db.User{IsAdmin: 1}
	if !svc.IsAdmin(admin) {
		t.Fatalf("expected admin user to be admin")
	}

	regular := &db.User{IsAdmin: 0}
	if svc.IsAdmin(regular) {
		t.Fatalf("expected regular user to be non-admin")
	}
}

func TestCanManageNotifier(t *testing.T) {
	svc := &Service{}

	admin := &db.User{ID: 1, IsAdmin: 1}
	if !svc.CanManageNotifier(admin, 999) {
		t.Fatalf("admin should manage any notifier")
	}

	owner := &db.User{ID: 42, IsAdmin: 0}
	if !svc.CanManageNotifier(owner, 42) {
		t.Fatalf("owner should manage own notifier")
	}

	other := &db.User{ID: 7, IsAdmin: 0}
	if svc.CanManageNotifier(other, 42) {
		t.Fatalf("non-owner should not manage notifier")
	}
}
