package panel

import (
	"strings"
	"testing"
	"time"
)

func TestUptimeBucketTitle(t *testing.T) {
	start := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)

	title := uptimeBucketTitle(start, end, 30*time.Minute, 0, 12)
	if !strings.Contains(title, "12 checks") {
		t.Fatalf("title = %q, want it to mention 12 checks", title)
	}
	if !strings.Contains(title, "100.00% uptime") {
		t.Fatalf("title = %q, want uptime percent", title)
	}

	title = uptimeBucketTitle(start, end, 0, 0, 1)
	if !strings.Contains(title, "1 check") {
		t.Fatalf("title = %q, want it to mention 1 check", title)
	}
	if !strings.Contains(title, "no uptime data") {
		t.Fatalf("title = %q, want no uptime data suffix", title)
	}

	title = uptimeBucketTitle(start, end, 0, 0, 0)
	if strings.Contains(title, "checks") {
		t.Fatalf("title = %q, must not mention checks when none were made", title)
	}
}

func TestCheckCountText(t *testing.T) {
	if got := checkCountText(1); got != "1 check" {
		t.Fatalf("checkCountText(1) = %q, want \"1 check\"", got)
	}
	if got := checkCountText(3); got != "3 checks" {
		t.Fatalf("checkCountText(3) = %q, want \"3 checks\"", got)
	}
}
