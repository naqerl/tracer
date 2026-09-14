package panel

import "testing"

func TestErrorCountText(t *testing.T) {
	if got := errorCountText(false, 3); got != "N/A" {
		t.Fatalf("errorCountText(false, 3) = %q, want N/A", got)
	}
	if got := errorCountText(true, 0); got != "0" {
		t.Fatalf("errorCountText(true, 0) = %q, want 0", got)
	}
	if got := errorCountText(true, 12); got != "12" {
		t.Fatalf("errorCountText(true, 12) = %q, want 12", got)
	}
	if got := errorCountText(true, 1); got != "1" {
		t.Fatalf("errorCountText(true, 1) = %q, want 1", got)
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
