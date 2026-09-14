package view

import (
	"testing"
	"time"
)

func TestFormatAgo(t *testing.T) {
	tests := []struct {
		age  time.Duration
		want string
	}{
		{0, "0s ago"},
		{30 * time.Second, "30s ago"},
		{59 * time.Second, "59s ago"},
		{2 * time.Minute, "2m ago"},
		{1*time.Hour + 53*time.Minute, "1h 53m ago"},
		{24 * time.Hour, "1d 0h ago"},
		{2*24*time.Hour + 5*time.Hour, "2d 5h ago"},
	}
	for _, tt := range tests {
		if got := FormatAgo(tt.age); got != tt.want {
			t.Fatalf("FormatAgo(%v) = %q, want %q", tt.age, got, tt.want)
		}
	}
}

func TestFormatAgo_NegativeIsZero(t *testing.T) {
	if got := FormatAgo(-time.Minute); got != "0s ago" {
		t.Fatalf("FormatAgo(-1m) = %q, want zero-ago", got)
	}
}

func TestLastUpdateClass(t *testing.T) {
	interval := 30 * time.Second
	tests := []struct {
		age  time.Duration
		want string
	}{
		{30 * time.Second, "text-fg-secondary"},
		{2 * interval, "text-fg-secondary"},
		{3 * interval, "text-yellow-400"},
		{5 * interval, "text-yellow-400"},
		{6 * interval, "text-red-400"},
	}
	for _, tt := range tests {
		if got := LastUpdateClass(tt.age, interval); got != tt.want {
			t.Fatalf("LastUpdateClass(%v, %v) = %q, want %q", tt.age, interval, got, tt.want)
		}
	}
}

func TestLastUpdateClass_ZeroIntervalDefaults(t *testing.T) {
	// 90s is within 2x..5x of a defaulted 30s interval, so it must be yellow.
	if got := LastUpdateClass(90*time.Second, 0); got != "text-yellow-400" {
		t.Fatalf("LastUpdateClass(90s, 0) = %q, want yellow with default 30s interval", got)
	}
}
