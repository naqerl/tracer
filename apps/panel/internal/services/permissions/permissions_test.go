package permissions

import (
	"testing"

	sharedtypes "tracer/libs/types"
)

func TestIsAdmin(t *testing.T) {
	svc := &Service{}

	if svc.IsAdmin(sharedtypes.User{}) {
		t.Fatalf("nil user must not be admin")
	}

	admin := sharedtypes.User{IsAdmin: true}
	if !svc.IsAdmin(admin) {
		t.Fatalf("expected admin user to be admin")
	}

	regular := sharedtypes.User{IsAdmin: false}
	if svc.IsAdmin(regular) {
		t.Fatalf("expected regular user to be non-admin")
	}
}

func TestCanManageNotifier(t *testing.T) {
	svc := &Service{}

	admin := sharedtypes.User{ID: 1, IsAdmin: true}
	if !svc.CanManageNotifier(admin, 999) {
		t.Fatalf("admin should manage any notifier")
	}

	owner := sharedtypes.User{ID: 42, IsAdmin: false}
	if !svc.CanManageNotifier(owner, 42) {
		t.Fatalf("owner should manage own notifier")
	}

	other := sharedtypes.User{ID: 7, IsAdmin: false}
	if svc.CanManageNotifier(other, 42) {
		t.Fatalf("non-owner should not manage notifier")
	}
}
