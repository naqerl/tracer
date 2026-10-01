package panel

import (
	"strings"
	"testing"
)

// TestNotFoundStatusKeepsReason proves a not-found result still carries the
// collector's own reason, so a missing service is distinguishable from a
// collector that cannot read its logs.
func TestNotFoundStatusKeepsReason(t *testing.T) {
	tests := []struct {
		name    string
		service string
		reason  string
		want    string
	}{
		{
			name:    "without_reason",
			service: "deploy/barse",
			reason:  "",
			want:    "deploy/barse was not found on the collector.",
		},
		{
			name:    "with_reason",
			service: "deploy/barse",
			reason:  "kubectl logs (default/deploy/barse): pods \"barse\" not found",
			want:    `deploy/barse was not found on the collector: kubectl logs (default/deploy/barse): pods "barse" not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := notFoundStatus(tt.service, tt.reason)
			if got != tt.want {
				t.Errorf("notFoundStatus() = %q, want %q", got, tt.want)
			}
			if !strings.Contains(got, tt.service) {
				t.Errorf("status must name the service, got %q", got)
			}
		})
	}
}
