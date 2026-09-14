package panel

import "testing"

func TestErrorCountText(t *testing.T) {
	if got := errorCountText(false, 3, 100); got != "N/A" {
		t.Fatalf("errorCountText(false, 3, 100) = %q, want N/A", got)
	}
	if got := errorCountText(true, 0, 100); got != "0.0%" {
		t.Fatalf("errorCountText(true, 0, 100) = %q, want 0.0%%", got)
	}
	if got := errorCountText(true, 12, 100); got != "12.0%" {
		t.Fatalf("errorCountText(true, 12, 100) = %q, want 12.0%%", got)
	}
	if got := errorCountText(true, 1, 10); got != "10.0%" {
		t.Fatalf("errorCountText(true, 1, 10) = %q, want 10.0%%", got)
	}
	if got := errorCountText(true, 0, 0); got != "0%" {
		t.Fatalf("errorCountText(true, 0, 0) = %q, want 0%%", got)
	}
}

func TestErrorCountClass(t *testing.T) {
	if got := errorCountClass(false, 5); got != "text-fg-secondary" {
		t.Fatalf("errorCountClass(false, 5) = %q, want muted", got)
	}
	if got := errorCountClass(true, 0); got != "text-fg-secondary" {
		t.Fatalf("errorCountClass(true, 0) = %q, want muted", got)
	}
	if got := errorCountClass(true, 1); got != "text-red-400" {
		t.Fatalf("errorCountClass(true, 1) = %q, want red", got)
	}
}
