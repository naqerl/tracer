package ingest

import (
	"context"
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
	cpuDeltaPercent float64
	memDeltaKB      int
	diskDeltaKB     int
	sampleRate      float64
}

func (c *testIngestConfig) GetCPUDeltaPercent() float64 { return c.cpuDeltaPercent }
func (c *testIngestConfig) GetMemDeltaKB() int          { return c.memDeltaKB }
func (c *testIngestConfig) GetDiskDeltaKB() int         { return c.diskDeltaKB }
func (c *testIngestConfig) GetSampleRate() float64      { return c.sampleRate }

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

func TestStoreLogErrorAlerts(t *testing.T) {
	mgr := testutil.NewTestDBManager(t)
	ch := make(chan notify.Notification, 32)
	cfg := &testIngestConfig{sampleRate: 1.0}
	svc := New(mgr, ch, nil, cfg)

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
	svc := New(mgr, ch, nil, cfg)

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
	svc := New(mgr, ch, nil, cfg)

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
