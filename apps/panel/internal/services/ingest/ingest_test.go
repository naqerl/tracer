package ingest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/services/notify"
	"tracer/apps/panel/internal/testutil"
	"tracer/libs/types"

	_ "modernc.org/sqlite"
)

// testIngestConfig implements IngestConfig for testing.
type testIngestConfig struct {
	cpuDeltaPercent       float64
	memDeltaKB            int
	diskDeltaKB           int
	sampleRate            float64
	cpuThresholdPercent   float64
	memThresholdPercent   float64
	diskThresholdPercent  float64
	resourceNotifyMinutes int
}

func (c *testIngestConfig) GetCPUDeltaPercent() float64      { return c.cpuDeltaPercent }
func (c *testIngestConfig) GetMemDeltaKB() int               { return c.memDeltaKB }
func (c *testIngestConfig) GetDiskDeltaKB() int              { return c.diskDeltaKB }
func (c *testIngestConfig) GetSampleRate() float64           { return c.sampleRate }
func (c *testIngestConfig) GetCPUThresholdPercent() float64  { return c.cpuThresholdPercent }
func (c *testIngestConfig) GetMemThresholdPercent() float64  { return c.memThresholdPercent }
func (c *testIngestConfig) GetDiskThresholdPercent() float64 { return c.diskThresholdPercent }
func (c *testIngestConfig) GetResourceNotifyMinutes() int {
	if c.resourceNotifyMinutes == 0 {
		return 10
	}
	return c.resourceNotifyMinutes
}

// notifyCounts tracks which notification types one StoreLog produced.
type notifyCounts struct {
	alerts int
}

// storeLogAndCount stores one log entry and drains the channel, counting the
// notification types it produced.
func storeLogAndCount(t *testing.T, svc *Service, ch chan notify.Notification, collector, app string, level types.LogLevel, message string) notifyCounts {
	t.Helper()

	_, err := svc.StoreLog(t.Context(), StoreLogRequest{
		CollectorName: collector,
		AppName:       app,
		Entry: types.LogEntry{
			Level:     level,
			Message:   message,
			Timestamp: time.Now(),
		},
	})
	if err != nil {
		t.Fatalf("store log: %v", err)
	}

	var counts notifyCounts
	for {
		select {
		case n := <-ch:
			switch n.Type {
			case notify.TypeAlert:
				counts.alerts++
			}
		default:
			return counts
		}
	}
}

// saveSystemStatsAndDrain saves stats and drains the channel, returning the
// single TypeResource notification payload if one was produced.
func saveSystemStatsAndDrain(t *testing.T, svc *Service, ch chan notify.Notification, collector string, stats types.Stats) (notify.ResourceEntry, bool) {
	t.Helper()

	if err := svc.SaveSystemStats(t.Context(), SaveSystemStatsRequest{
		CollectorName: collector,
		Stats:         stats,
	}); err != nil {
		t.Fatalf("save stats: %v", err)
	}

	for {
		select {
		case n := <-ch:
			if n.Type != notify.TypeResource {
				t.Fatalf("unexpected notification type %s", n.Type)
			}
			entry, ok := n.Payload.(notify.ResourceEntry)
			if !ok {
				t.Fatalf("expected ResourceEntry payload, got %T", n.Payload)
			}
			return entry, true
		default:
			return notify.ResourceEntry{}, false
		}
	}
}

// TestResourceThresholdNotification fires a notification when CPU exceeds the
// threshold, suppresses repeats during the cooldown, and re-fires the same
// resource after it recovers and breaches again.
func TestResourceThresholdNotification(t *testing.T) {
	mgr := testutil.NewTestDBManager(t)
	ch := make(chan notify.Notification, 32)
	cfg := &testIngestConfig{
		sampleRate:          1.0,
		cpuDeltaPercent:     1.0,
		cpuThresholdPercent: 90.0,
	}
	svc := New(mgr, ch, nil, cfg, nil)

	if err := svc.RegisterCollector(t.Context(), RegisterCollectorRequest{
		CollectorName: "test-collector",
		Apps:          []string{"test-app"},
	}); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	now := time.Now().Truncate(time.Second).Unix()
	build := func(cpu float64, ts int64) types.Stats {
		return types.Stats{
			CPUUsagePercent: cpu,
			MemTotalMB:      8192,
			MemUsedMB:       2048,
			MemAvailableMB:  6144,
			DiskTotalGB:     250,
			DiskUsedGB:      100,
			DiskAvailGB:     150,
			Timestamp:       time.Unix(ts, 0),
		}
	}

	// Below threshold: no notification.
	if _, fired := saveSystemStatsAndDrain(t, svc, ch, "test-collector", build(50.0, now)); fired {
		t.Fatal("below threshold: expected no notification")
	}

	// Above threshold: notification fires with the breached resource.
	entry, fired := saveSystemStatsAndDrain(t, svc, ch, "test-collector", build(97.0, now+1))
	if !fired {
		t.Fatal("above threshold: expected a notification")
	}
	if len(entry.Breached) != 1 || entry.Breached[0] != "cpu" {
		t.Fatalf("expected breached [cpu], got %v", entry.Breached)
	}
	if entry.CPUUsagePercent != 97.0 {
		t.Fatalf("expected CPU 97, got %v", entry.CPUUsagePercent)
	}

	// Still above threshold but within the cooldown: suppressed.
	if _, fired := saveSystemStatsAndDrain(t, svc, ch, "test-collector", build(98.0, now+2)); fired {
		t.Fatal("within cooldown: expected no notification")
	}

	// Recovers below threshold, then breaches again: notifies immediately.
	if _, fired := saveSystemStatsAndDrain(t, svc, ch, "test-collector", build(50.0, now+3)); fired {
		t.Fatal("recovery: expected no notification")
	}
	if _, fired := saveSystemStatsAndDrain(t, svc, ch, "test-collector", build(95.0, now+4)); !fired {
		t.Fatal("re-breach after recovery: expected a notification")
	}
}

func TestStoreLogErrorAlerts(t *testing.T) {
	mgr := testutil.NewTestDBManager(t)
	ch := make(chan notify.Notification, 32)
	cfg := &testIngestConfig{sampleRate: 1.0}
	svc := New(mgr, ch, nil, cfg, nil)

	if err := svc.RegisterCollector(t.Context(), RegisterCollectorRequest{
		CollectorName: "test-collector",
		Apps:          []string{"test-app"},
	}); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	// Every error entry alerts, including repeats of the same message.
	counts := storeLogAndCount(t, svc, ch, "test-collector", "test-app", types.LevelError, "boom: connection refused")
	if counts.alerts != 1 {
		t.Fatalf("first error: expected 1 alert, got %+v", counts)
	}
	counts = storeLogAndCount(t, svc, ch, "test-collector", "test-app", types.LevelError, "boom: connection refused")
	if counts.alerts != 1 {
		t.Fatalf("second error of same group: expected 1 alert, got %+v", counts)
	}

	// Non-error levels never alert.
	counts = storeLogAndCount(t, svc, ch, "test-collector", "test-app", types.LevelWarn, "boom: timeout")
	if counts.alerts != 0 {
		t.Fatalf("warn: expected no notifications, got %+v", counts)
	}
	counts = storeLogAndCount(t, svc, ch, "test-collector", "test-app", types.LevelInfo, "boom: connection refused")
	if counts.alerts != 0 {
		t.Fatalf("info: expected no notifications, got %+v", counts)
	}
}

func TestStoreLogAfterRetentionPruneDoesNotReannounceGroup(t *testing.T) {
	mgr := testutil.NewTestDBManager(t)
	ch := make(chan notify.Notification, 32)
	cfg := &testIngestConfig{sampleRate: 1.0}
	svc := New(mgr, ch, nil, cfg, nil)

	if err := svc.RegisterCollector(t.Context(), RegisterCollectorRequest{
		CollectorName: "test-collector",
		Apps:          []string{"test-app"},
	}); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	counts := storeLogAndCount(t, svc, ch, "test-collector", "test-app", types.LevelError, "boom: connection refused")
	if counts.alerts != 1 {
		t.Fatalf("first error: expected 1 alert, got %+v", counts)
	}

	// Simulate retention pruning: the log rows are deleted and last_log_at is
	// reset to 0, but the group row persists. No public API exposes this yet,
	// so the queries run directly against the app DB.
	handle, err := mgr.GetAppDB("test-collector", "test-app")
	if err != nil {
		t.Fatalf("get app db: %v", err)
	}
	if err := handle.Write(t.Context(), func(tx db.DBTX) error {
		q := db.New(tx)
		if err := q.DeleteOldLogs(t.Context(), time.Now().Add(time.Hour).Unix()); err != nil {
			return err
		}
		return q.RecalculateLogGroupCounters(t.Context())
	}); err != nil {
		t.Fatalf("simulate retention prune: %v", err)
	}

	// The re-emerging error after a retention prune still alerts; dedup is
	// the notifier's job, not the ingest service's.
	counts = storeLogAndCount(t, svc, ch, "test-collector", "test-app", types.LevelError, "boom: connection refused")
	if counts.alerts != 1 {
		t.Fatalf("after retention prune: expected 1 alert, got %+v", counts)
	}
}

// drainNotifications reads every notification currently queued on ch.
func drainNotifications(ch chan notify.Notification) []notify.Notification {
	var ns []notify.Notification
	for {
		select {
		case n := <-ch:
			ns = append(ns, n)
		default:
			return ns
		}
	}
}

// TestStoreLogNotificationPayloadMapping locks in the payload-type contract
// per notification type: log-derived notifications carry a notify.LogEntry,
// lifecycle notifications carry a plain string.
func TestStoreLogNotificationPayloadMapping(t *testing.T) {
	mgr := testutil.NewTestDBManager(t)
	ch := make(chan notify.Notification, 32)
	cfg := &testIngestConfig{sampleRate: 1.0}
	svc := New(mgr, ch, nil, cfg, nil)

	if err := svc.RegisterCollector(t.Context(), RegisterCollectorRequest{
		CollectorName: "test-collector",
		Apps:          []string{"test-app"},
	}); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	// First error → TypeAlert with a LogEntry payload.
	if _, err := svc.StoreLog(t.Context(), StoreLogRequest{
		CollectorName: "test-collector",
		AppName:       "test-app",
		Entry: types.LogEntry{
			Level:     types.LevelError,
			Message:   "boom: connection refused",
			Timestamp: time.Now(),
		},
	}); err != nil {
		t.Fatalf("store log: %v", err)
	}
	ns := drainNotifications(ch)
	if len(ns) != 1 || ns[0].Type != notify.TypeAlert {
		t.Fatalf("expected a single TypeAlert notification, got %+v", ns)
	}
	entry, ok := ns[0].Payload.(notify.LogEntry)
	if !ok {
		t.Fatalf("TypeAlert: expected LogEntry payload, got %T", ns[0].Payload)
	}
	if entry.Level != types.LevelError || entry.Message != "boom: connection refused" || entry.GroupID != 1 || entry.LogID == 0 || entry.CreatedAt.IsZero() {
		t.Errorf("TypeAlert: unexpected LogEntry: %+v", entry)
	}

	// Later error of the same group → TypeAlert with a LogEntry payload.
	if _, err := svc.StoreLog(t.Context(), StoreLogRequest{
		CollectorName: "test-collector",
		AppName:       "test-app",
		Entry: types.LogEntry{
			Level:     types.LevelError,
			Message:   "boom: connection refused",
			Timestamp: time.Now(),
		},
	}); err != nil {
		t.Fatalf("store log: %v", err)
	}
	ns = drainNotifications(ch)
	if len(ns) != 1 || ns[0].Type != notify.TypeAlert {
		t.Fatalf("expected a single TypeAlert notification, got %+v", ns)
	}
	entry, ok = ns[0].Payload.(notify.LogEntry)
	if !ok {
		t.Fatalf("TypeAlert: expected LogEntry payload, got %T", ns[0].Payload)
	}
	if entry.Level != types.LevelError || entry.Message != "boom: connection refused" || entry.GroupID != 1 || entry.LogID == 0 || entry.CreatedAt.IsZero() {
		t.Errorf("TypeAlert: unexpected LogEntry: %+v", entry)
	}

	// Lifecycle → TypeLifecycle with a string payload.
	if err := svc.StoreLifecycle(t.Context(), StoreLifecycleRequest{
		CollectorName: "test-collector",
		AppName:       "test-app",
		Kind:          types.LifecycleRestart,
		RawLine:       "restart",
		Timestamp:     time.Now(),
	}); err != nil {
		t.Fatalf("store lifecycle: %v", err)
	}
	ns = drainNotifications(ch)
	if len(ns) != 1 || ns[0].Type != notify.TypeLifecycle {
		t.Fatalf("expected a single TypeLifecycle notification, got %+v", ns)
	}
	if payload, ok := ns[0].Payload.(string); !ok || payload != "restart" {
		t.Fatalf("TypeLifecycle: expected string payload %q, got %v (%T)", "restart", ns[0].Payload, ns[0].Payload)
	}
}

func TestRuntimeConfigDeltaChange(t *testing.T) {
	ctx := context.Background()
	mgr := testutil.NewTestDBManager(t)

	cfg := &testIngestConfig{
		sampleRate:      1.0,
		cpuDeltaPercent: 50.0,
		memDeltaKB:      100000,
		diskDeltaKB:     100000,
	}

	svc := New(mgr, nil, nil, cfg, nil)

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

func TestResourceConfigResolverFallsBackToGlobals(t *testing.T) {
	mgr := testutil.NewTestDBManager(t)
	globals := ResourceConfig{
		CPUDeltaPercent:       5.0,
		MemDeltaKB:            256,
		DiskDeltaKB:           4096,
		CPUThresholdPercent:   80.0,
		MemThresholdPercent:   70.0,
		DiskThresholdPercent:  90.0,
		ResourceNotifyMinutes: 30,
	}
	resolver := NewResourceConfigResolver(mgr, globals)

	handle, err := mgr.CreateCollectorDB("res-collector")
	if err != nil {
		t.Fatalf("create collector db: %v", err)
	}
	_ = handle // only existence matters

	got := resolver.ResolveResourceConfig("res-collector")
	if got.CPUDeltaPercent != globals.CPUDeltaPercent {
		t.Fatalf("CPUDeltaPercent: got %v, want %v", got.CPUDeltaPercent, globals.CPUDeltaPercent)
	}
	if got.MemDeltaKB != globals.MemDeltaKB {
		t.Fatalf("MemDeltaKB: got %v, want %v", got.MemDeltaKB, globals.MemDeltaKB)
	}
	if got.ResourceNotifyMinutes != globals.ResourceNotifyMinutes {
		t.Fatalf("ResourceNotifyMinutes: got %v, want %v", got.ResourceNotifyMinutes, globals.ResourceNotifyMinutes)
	}
}

func TestResourceConfigResolverAppliesOverrides(t *testing.T) {
	mgr := testutil.NewTestDBManager(t)
	globals := ResourceConfig{
		CPUDeltaPercent:       5.0,
		MemDeltaKB:            256,
		DiskDeltaKB:           4096,
		CPUThresholdPercent:   80.0,
		MemThresholdPercent:   70.0,
		DiskThresholdPercent:  90.0,
		ResourceNotifyMinutes: 30,
	}
	resolver := NewResourceConfigResolver(mgr, globals)

	handle, err := mgr.CreateCollectorDB("override-collector")
	if err != nil {
		t.Fatalf("create collector db: %v", err)
	}

	cpuDelta := 0.5
	memDelta := 32
	notifyMin := 5
	cfg := types.CollectorConfig{
		Stats: &types.StatsConfig{
			CPUDeltaPercent:       &cpuDelta,
			MemDeltaKB:            &memDelta,
			ResourceNotifyMinutes: &notifyMin,
		},
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := handle.Write(context.Background(), func(tx db.DBTX) error {
		return db.New(tx).UpsertCollectorConfig(context.Background(), db.UpsertCollectorConfigParams{
			ConfigJson: string(cfgJSON),
			UpdatedAt:  time.Now().Unix(),
		})
	}); err != nil {
		t.Fatalf("upsert config: %v", err)
	}

	got := resolver.ResolveResourceConfig("override-collector")
	if got.CPUDeltaPercent != cpuDelta {
		t.Fatalf("CPUDeltaPercent: got %v, want %v", got.CPUDeltaPercent, cpuDelta)
	}
	if got.MemDeltaKB != memDelta {
		t.Fatalf("MemDeltaKB: got %v, want %v", got.MemDeltaKB, memDelta)
	}
	if got.ResourceNotifyMinutes != notifyMin {
		t.Fatalf("ResourceNotifyMinutes: got %v, want %v", got.ResourceNotifyMinutes, notifyMin)
	}
	// Non-overridden fields stay at globals.
	if got.DiskDeltaKB != globals.DiskDeltaKB {
		t.Fatalf("DiskDeltaKB: got %v, want %v", got.DiskDeltaKB, globals.DiskDeltaKB)
	}
	if got.CPUThresholdPercent != globals.CPUThresholdPercent {
		t.Fatalf("CPUThresholdPercent: got %v, want %v", got.CPUThresholdPercent, globals.CPUThresholdPercent)
	}
}

func TestPerCollectorOverridesAffectDeltaAndThresholds(t *testing.T) {
	mgr := testutil.NewTestDBManager(t)
	ch := make(chan notify.Notification, 32)
	// High globals: change of 0.5 CPU, 4KB mem, 1GB disk won't create periods.
	cfg := &testIngestConfig{
		sampleRate:          1.0,
		cpuDeltaPercent:     5.0,
		memDeltaKB:          100000,
		diskDeltaKB:         100000,
		cpuThresholdPercent: 95.0,
	}

	// Resolver globals match the ingest config defaults, so the baseline
	// collector (no per-collector override) gets the same high thresholds.
	targetResolver := NewResourceConfigResolver(mgr, ResourceConfig{
		CPUDeltaPercent:       cfg.cpuDeltaPercent,
		MemDeltaKB:            cfg.memDeltaKB,
		DiskDeltaKB:           cfg.diskDeltaKB,
		CPUThresholdPercent:   cfg.cpuThresholdPercent,
		MemThresholdPercent:   cfg.memThresholdPercent,
		DiskThresholdPercent:  cfg.diskThresholdPercent,
		ResourceNotifyMinutes: cfg.resourceNotifyMinutes,
	})
	svc := New(mgr, ch, nil, cfg, targetResolver)

	if err := svc.RegisterCollector(t.Context(), RegisterCollectorRequest{
		CollectorName: "target",
		Apps:          []string{"app"},
	}); err != nil {
		t.Fatalf("register target: %v", err)
	}

	// Write a per-collector config with low deltas into the target's collector DB.
	targetHandle, err := mgr.GetCollectorDB("target")
	if err != nil {
		t.Fatalf("get target db: %v", err)
	}
	cpuDelta := 0.1
	memDelta := 1
	diskDelta := 1
	targetCfg := types.CollectorConfig{
		Stats: &types.StatsConfig{
			CPUDeltaPercent: &cpuDelta,
			MemDeltaKB:      &memDelta,
			DiskDeltaKB:     &diskDelta,
		},
	}
	cfgJSON, _ := json.Marshal(targetCfg)
	if err := targetHandle.Write(t.Context(), func(tx db.DBTX) error {
		return db.New(tx).UpsertCollectorConfig(t.Context(), db.UpsertCollectorConfigParams{
			ConfigJson: string(cfgJSON),
			UpdatedAt:  time.Now().Unix(),
		})
	}); err != nil {
		t.Fatalf("upsert target config: %v", err)
	}
	if err := svc.RegisterCollector(t.Context(), RegisterCollectorRequest{
		CollectorName: "baseline",
		Apps:          []string{"app"},
	}); err != nil {
		t.Fatalf("register baseline: %v", err)
	}

	now := time.Now().Truncate(time.Second)

	// Seed both collectors with identical initial stats.
	for _, collector := range []string{"target", "baseline"} {
		if err := svc.SaveSystemStats(t.Context(), SaveSystemStatsRequest{
			CollectorName: collector,
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
			t.Fatalf("seed stats %s: %v", collector, err)
		}
	}

	// Apply a small change that exceeds the target's overrides but not the baseline's globals.
	for _, collector := range []string{"target", "baseline"} {
		if err := svc.SaveSystemStats(t.Context(), SaveSystemStatsRequest{
			CollectorName: collector,
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
			t.Fatalf("second save %s: %v", collector, err)
		}
	}

	history, err := svc.GetStatsHistory(t.Context(), "target", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetStatsHistory target: %v", err)
	}
	if len(history.CPUPeriods) != 2 {
		t.Fatalf("target: expected 2 CPU periods (override delta), got %d", len(history.CPUPeriods))
	}

	history, err = svc.GetStatsHistory(t.Context(), "baseline", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("GetStatsHistory baseline: %v", err)
	}
	if len(history.CPUPeriods) != 1 {
		t.Fatalf("baseline: expected 1 CPU period (global delta), got %d", len(history.CPUPeriods))
	}
}
