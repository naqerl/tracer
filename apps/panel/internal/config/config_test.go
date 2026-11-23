package config

import "testing"

func TestDefaultResourceThresholds(t *testing.T) {
	cfg := Default()

	if cfg.CPUThresholdPercent != 95.0 {
		t.Errorf("CPU threshold: got %v, want 95", cfg.CPUThresholdPercent)
	}
	if cfg.MemThresholdPercent != 95.0 {
		t.Errorf("memory threshold: got %v, want 95", cfg.MemThresholdPercent)
	}
	if cfg.DiskThresholdPercent != 95.0 {
		t.Errorf("disk threshold: got %v, want 95", cfg.DiskThresholdPercent)
	}
	if cfg.ResourceNotifyMinutes != 10 {
		t.Errorf("resource notification cooldown: got %d, want 10", cfg.ResourceNotifyMinutes)
	}
}
