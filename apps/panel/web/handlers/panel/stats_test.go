package panel

import "testing"

func TestErrorCountText(t *testing.T) {
	if got := errorCountText(false, false, 3); got != "" {
		t.Fatalf("errorCountText(false, false, 3) = %q, want empty", got)
	}
	if got := errorCountText(false, true, 0); got != "0" {
		t.Fatalf("errorCountText(false, true, 0) = %q, want 0", got)
	}
	if got := errorCountText(false, true, 12); got != "12" {
		t.Fatalf("errorCountText(false, true, 12) = %q, want 12", got)
	}
	if got := errorCountText(false, true, 1); got != "1" {
		t.Fatalf("errorCountText(false, true, 1) = %q, want 1", got)
	}
	// Log source configured, no logs received yet — show "0".
	if got := errorCountText(true, false, 0); got != "0" {
		t.Fatalf("errorCountText(true, false, 0) = %q, want 0", got)
	}
	// Log source configured, logs received, zero errors.
	if got := errorCountText(true, true, 0); got != "0" {
		t.Fatalf("errorCountText(true, true, 0) = %q, want 0", got)
	}
	// Log source configured, logs received, errors present.
	if got := errorCountText(true, true, 5); got != "5" {
		t.Fatalf("errorCountText(true, true, 5) = %q, want 5", got)
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
