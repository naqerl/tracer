package view

import (
	"reflect"
	"testing"

	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/web/templates"
)

// TestGroupScopeEntries verifies that a flat list of collector/app entries is
// grouped by collector, sorting collectors and the apps within each collector
// alphabetically.
func TestGroupScopeEntries(t *testing.T) {
	entries := []domain.ScopeEntry{
		{Collector: "prod", App: "api", Granted: true},
		{Collector: "staging", App: "web", Granted: false},
		{Collector: "prod", App: "worker", Granted: false},
		{Collector: "staging", App: "db", Granted: true},
		{Collector: "prod", App: "api", Granted: true}, // duplicate row
	}

	gotOrder, got := groupScopeEntries(entries)

	wantOrder := []string{"prod", "staging"}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Fatalf("groupScopeEntries order = %v, want %v", gotOrder, wantOrder)
	}

	want := map[string][]domain.ScopeEntry{
		"prod": {
			{Collector: "prod", App: "api", Granted: true},
			{Collector: "prod", App: "api", Granted: true},
			{Collector: "prod", App: "worker", Granted: false},
		},
		"staging": {
			{Collector: "staging", App: "db", Granted: true},
			{Collector: "staging", App: "web", Granted: false},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groupScopeEntries = %v, want %v", got, want)
	}
}

// TestAllGranted verifies a collector square is [x] only when every app row is
// granted and there is at least one app.
func TestAllGranted(t *testing.T) {
	cases := []struct {
		name string
		apps []domain.ScopeEntry
		want bool
	}{
		{name: "empty", apps: nil, want: false},
		{name: "all granted", apps: []domain.ScopeEntry{{Granted: true}, {Granted: true}}, want: true},
		{name: "one not granted", apps: []domain.ScopeEntry{{Granted: true}, {Granted: false}}, want: false},
	}
	for _, tc := range cases {
		if got := allGranted(tc.apps); got != tc.want {
			t.Errorf("%s: allGranted = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSanitizeID confirms collector/app names are safe to embed in element ids.
func TestSanitizeID(t *testing.T) {
	cases := map[string]string{
		"prod":      "prod",
		"my-app.x":  "my-app-x",
		"host/name": "host-name",
		"UPPER_1-2": "UPPER_1-2",
	}
	for in, want := range cases {
		if got := templates.SanitizeID(in); got != want {
			t.Errorf("SanitizeID(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestScopeTreeIDCollisionFreesIds verifies that names sanitizing to the same
// id still yield distinct element ids via the per-row index.
func TestScopeTreeIDCollisionFreesIds(t *testing.T) {
	if scopeTreeCollectorID("a/b", 0) == scopeTreeCollectorID("a-b", 1) {
		t.Fatal("collector ids must stay distinct across indexes")
	}
	if scopeTreeAppID("a/b", 0, "x", 0) == scopeTreeAppID("a-b", 1, "x", 0) {
		t.Fatal("app ids must stay distinct across collector indexes")
	}
	if scopeTreeAppID("c", 0, "a/b", 0) == scopeTreeAppID("c", 0, "a-b", 1) {
		t.Fatal("app ids must stay distinct across app indexes")
	}
}
