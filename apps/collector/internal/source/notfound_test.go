package source

import (
	"errors"
	"strings"
	"testing"
)

// TestIsNoSuchResource separates an absent resource from a source that exists
// but could not be read. Reporting the latter as "not found" is what made an
// unreadable kubeconfig look like a missing deployment.
func TestIsNoSuchResource(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			name:   "not found",
			stderr: `Error from server (NotFound): pods "barse" not found`,
			want:   true,
		},
		{
			name:   "deployment not found",
			stderr: `error: deployment.apps "barse" not found`,
			want:   true,
		},
		{
			name:   "unknown resource type",
			stderr: `error: the server doesn't have a resource type "barse"`,
			want:   true,
		},
		{
			name:   "unreadable kubeconfig",
			stderr: `error: error loading config file "/etc/rancher/k3s/k3s.yaml": open /etc/rancher/k3s/k3s.yaml: permission denied`,
			want:   false,
		},
		{
			name:   "forbidden resource",
			stderr: `Error from server (Forbidden): pods "barse" is forbidden: User "tracer-collector" cannot get resource "pods" in API group "" in the namespace "default"`,
			want:   false,
		},
		{
			name:   "connection refused",
			stderr: `The connection to the server localhost:8080 was refused - did you specify the right host or port?`,
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNoSuchResource(tt.stderr); got != tt.want {
				t.Errorf("isNoSuchResource(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

// TestIsNoSuchUnit separates an absent systemd unit from a journalctl failure
// such as a missing permission.
func TestIsNoSuchUnit(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{name: "no such unit", stderr: "No journal files were found.", want: false},
		{name: "unit missing", stderr: `Failed to add match: Unit xyz.service not found.`, want: true},
		{name: "no entries", stderr: "-- No entries --", want: true},
		{
			name:   "permission denied",
			stderr: "Failed to open journal: Permission denied",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNoSuchUnit(tt.stderr); got != tt.want {
				t.Errorf("isNoSuchUnit(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

// TestNotFoundWrapsSentinel proves the marker stays inspectable through the
// wrapping, which is what lets the collector tell the two apart.
func TestNotFoundWrapsSentinel(t *testing.T) {
	err := NotFound(errors.New("no such unit"))

	if !errors.Is(err, ErrNotFound) {
		t.Error("expected the wrapped error to match ErrNotFound")
	}
	if !strings.Contains(err.Error(), "no such unit") {
		t.Errorf("expected the original reason to survive, got %q", err.Error())
	}
	if NotFound(nil) != nil {
		t.Error("expected NotFound(nil) to stay nil")
	}
}
