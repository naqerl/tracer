package topology

import (
	"testing"

	"tracer/apps/panel/internal/domain"
	panelsvc "tracer/apps/panel/internal/services/panel"
)

func TestGetSnapshotDeniesNonUserActors(t *testing.T) {
	svc := NewService(nil) // nil panel: denied actors return before touching it

	cases := []struct {
		name  string
		actor domain.Actor
	}{
		{"system", domain.System{}},
		{"unauthenticated", domain.Unauthenticated{}},
	}
	for _, c := range cases {
		if _, err := svc.GetSnapshot(t.Context(), c.actor); err != panelsvc.ErrAccessDenied {
			t.Errorf("%s: expected ErrAccessDenied, got %v", c.name, err)
		}
	}
}
