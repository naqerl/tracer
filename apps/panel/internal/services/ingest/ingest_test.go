package ingest

import (
	"context"
	"testing"
	"time"

	"tracer/apps/panel/internal/dbmanager"
	"tracer/libs/types"

	_ "modernc.org/sqlite"
)

// testIngestConfig implements IngestConfig for testing.
type testIngestConfig struct {
	cpuDeltaPercent float64
	memDeltaKB      int
	diskDeltaKB     int
	sampleRate      float64
}

func (c *testIngestConfig) GetCPUDeltaPercent() float64 { return c.cpuDeltaPercent }
func (c *testIngestConfig) GetMemDeltaKB() int          { return c.memDeltaKB }
func (c *testIngestConfig) GetDiskDeltaKB() int         { return c.diskDeltaKB }
func (c *testIngestConfig) GetSampleRate() float64      { return c.sampleRate }

func newTestDBManager(t *testing.T) *dbmanager.Manager {
	t.Helper()
	dir := t.TempDir()
	mgr, err := dbmanager.New(dir)
	if err != nil {
		t.Fatalf("create dbmanager: %v", err)
	}
	return mgr
}

func TestRuntimeConfigDeltaChange(t *testing.T) {
	ctx := context.Background()
	mgr := newTestDBManager(t)

	cfg := &testIngestConfig{
		sampleRate:      1.0,
		cpuDeltaPercent: 50.0,
		memDeltaKB:      100000,
		diskDeltaKB:     100000,
	}

	svc := New(mgr, nil, nil, cfg)

	if err := svc.RegisterCollector(ctx, RegisterCollectorRequest{
		CollectorName: "test-collector",
		Apps:          []string{"test-app"},
	}); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	now := time.Now().Truncate(time.Second)

	// First save: creates open periods
	if err := svc.SaveSystemStats(ctx, SaveSystemStatsRequest{
		CollectorName: "test-collector",
		Stats: types.Stats{
			CPUUsagePercent: 50.0,
			MemTotalMB:      8192,
			MemUsedMB:       2048,
			MemAvailableMB:  6144,
			DiskTotalGB:     250,
			DiskUsedGB:      100,
			DiskAvailGB:     150,
			Timestamp:       now,
		},
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// Small change — below high thresholds, should NOT create new periods
	if err := svc.SaveSystemStats(ctx, SaveSystemStatsRequest{
		CollectorName: "test-collector",
		Stats: types.Stats{
			CPUUsagePercent: 50.5,
			MemTotalMB:      8192,
			MemUsedMB:       2050,
			MemAvailableMB:  6142,
			DiskTotalGB:     250,
			DiskUsedGB:      100,
			DiskAvailGB:     150,
			Timestamp:       now.Add(time.Second),
		},
	}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	history, err := svc.GetStatsHistory(ctx, "test-collector", now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetStatsHistory: %v", err)
	}

	if len(history.CPUPeriods) != 1 {
		t.Fatalf("before config change: expected 1 CPU period, got %d", len(history.CPUPeriods))
	}
	if len(history.MemPeriods) != 1 {
		t.Fatalf("before config change: expected 1 Mem period, got %d", len(history.MemPeriods))
	}
	if len(history.DiskPeriods) != 1 {
		t.Fatalf("before config change: expected 1 Disk period, got %d", len(history.DiskPeriods))
	}

	// Lower thresholds — same change should now exceed them
	cfg.cpuDeltaPercent = 0.1
	cfg.memDeltaKB = 1
	cfg.diskDeltaKB = 1

	// Same small change — now above low thresholds, should create new periods
	if err := svc.SaveSystemStats(ctx, SaveSystemStatsRequest{
		CollectorName: "test-collector",
		Stats: types.Stats{
			CPUUsagePercent: 51.0,
			MemTotalMB:      8192,
			MemUsedMB:       2055,
			MemAvailableMB:  6137,
			DiskTotalGB:     250,
			DiskUsedGB:      101,
			DiskAvailGB:     149,
			Timestamp:       now.Add(2 * time.Second),
		},
	}); err != nil {
		t.Fatalf("third save: %v", err)
	}

	history, err = svc.GetStatsHistory(ctx, "test-collector", now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetStatsHistory: %v", err)
	}

	if len(history.CPUPeriods) != 2 {
		t.Fatalf("after config change: expected 2 CPU periods, got %d", len(history.CPUPeriods))
	}
	if len(history.MemPeriods) != 2 {
		t.Fatalf("after config change: expected 2 Mem periods, got %d", len(history.MemPeriods))
	}
	if len(history.DiskPeriods) != 2 {
		t.Fatalf("after config change: expected 2 Disk periods, got %d", len(history.DiskPeriods))
	}
}
