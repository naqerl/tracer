package view

import "testing"

func TestStatValue(t *testing.T) {
	if got := statValue(false, 5); got != "N/A" {
		t.Fatalf("statValue(false, 5) = %q, want \"N/A\"", got)
	}
	if got := statValue(true, 0); got != "0" {
		t.Fatalf("statValue(true, 0) = %q, want \"0\"", got)
	}
	if got := statValue(true, 42); got != "42" {
		t.Fatalf("statValue(true, 42) = %q, want \"42\"", got)
	}
}

func TestDropValue(t *testing.T) {
	if got := dropValue(0); got != "0" {
		t.Fatalf("dropValue(0) = %q, want \"0\"", got)
	}
	if got := dropValue(42); got != "42" {
		t.Fatalf("dropValue(42) = %q, want \"42\"", got)
	}
}

func TestStatClass(t *testing.T) {
	tests := []struct {
		hasLogs bool
		v       int64
		accent  string
		want    string
	}{
		{hasLogs: false, v: 7, accent: "text-red-400", want: "text-fg-secondary"},
		{hasLogs: true, v: 0, accent: "text-red-400", want: "text-fg-secondary"},
		{hasLogs: true, v: 0, accent: "", want: "text-fg-secondary"},
		{hasLogs: true, v: 3, accent: "text-red-400", want: "text-red-400"},
		{hasLogs: true, v: 3, accent: "", want: "text-fg-secondary"},
	}
	for _, tt := range tests {
		if got := statClass(tt.hasLogs, tt.v, tt.accent); got != tt.want {
			t.Fatalf("statClass(%v, %d, %q) = %q, want %q", tt.hasLogs, tt.v, tt.accent, got, tt.want)
		}
	}
}
