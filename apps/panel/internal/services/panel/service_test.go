package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
	ingestsvc "tracer/apps/panel/internal/services/ingest"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/testutil"
	"tracer/libs/types"
)

// testIngestConfig implements ingest.IngestConfig for testing.
type testIngestConfig struct {
	cpuDeltaPercent      float64
	memDeltaKB           int
	diskDeltaKB          int
	sampleRate           float64
	cpuThresholdPercent  float64
	memThresholdPercent  float64
	diskThresholdPercent float64
}

func (c *testIngestConfig) GetCPUDeltaPercent() float64      { return c.cpuDeltaPercent }
func (c *testIngestConfig) GetMemDeltaKB() int               { return c.memDeltaKB }
func (c *testIngestConfig) GetDiskDeltaKB() int              { return c.diskDeltaKB }
func (c *testIngestConfig) GetSampleRate() float64           { return c.sampleRate }
func (c *testIngestConfig) GetCPUThresholdPercent() float64  { return c.cpuThresholdPercent }
func (c *testIngestConfig) GetMemThresholdPercent() float64  { return c.memThresholdPercent }
func (c *testIngestConfig) GetDiskThresholdPercent() float64 { return c.diskThresholdPercent }
func (c *testIngestConfig) GetResourceNotifyMinutes() int    { return 10 }

// setupTestService creates a fully wired panel Service for testing.
// It creates a test user (admin), registers a collector+app DB, and grants access.
func setupTestService(t *testing.T) (*Service, *dbmanager.DBHandle, int64, string, string) {
	t.Helper()
	ctx := t.Context()

	// --- Panel DB ---
	panelDB := testutil.NewTestDB(t)

	// --- DBManager ---
	mgr := testutil.NewTestDBManager(t)

	// --- Tracker ---
	tracker := ingestsvc.NewTracker()

	// --- User service ---
	userSvc := usersvc.NewService(panelDB)

	// --- Auth service (to create user) ---
	// Use the minimum bcrypt cost: cost 12 is ~250ms per hash, which would
	// add ~30s to the suite across all tests.
	authSvc := authsvc.New(panelDB, testutil.TestKeySecret(), bcrypt.MinCost)

	user, err := authSvc.Register(ctx, authsvc.RegisterRequest{
		Username: "admin",
		Password: "secret123",
	})
	if err != nil {
		t.Fatalf("register admin user: %v", err)
	}

	// --- Grant access ---
	if err := userSvc.GrantAccess(ctx, user.ID, usersvc.AppAccess{
		CollectorName: "test-collector",
		AppName:       "test-app",
	}); err != nil {
		t.Fatalf("grant access: %v", err)
	}

	// --- Register collector (creates DBs) ---
	ingestSvc := ingestsvc.New(mgr, nil, nil, &testIngestConfig{
		sampleRate:      1.0,
		cpuDeltaPercent: 1.0,
		memDeltaKB:      64,
		diskDeltaKB:     1024,
	}, nil)
	if err := ingestSvc.RegisterCollector(ctx, ingestsvc.RegisterCollectorRequest{
		CollectorName: "test-collector",
		Apps:          []string{"test-app"},
	}); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	// --- Create the panel service ---
	svc := New(mgr, ingestSvc, tracker, userSvc, &fakeConfigPusher{}, nil)

	return svc, panelDB, user.ID, "test-collector", "test-app"
}

// setupTestServiceNoAccess creates a service with a user that has no access grants.
func setupTestServiceNoAccess(t *testing.T) (*Service, int64) {
	t.Helper()
	ctx := t.Context()

	panelDB := testutil.NewTestDB(t)
	mgr := testutil.NewTestDBManager(t)
	tracker := ingestsvc.NewTracker()
	userSvc := usersvc.NewService(panelDB)
	authSvc := authsvc.New(panelDB, testutil.TestKeySecret(), bcrypt.MinCost)

	// Register collector anyway (so it exists)
	ingestSvc := ingestsvc.New(mgr, nil, nil, &testIngestConfig{
		sampleRate:      1.0,
		cpuDeltaPercent: 1.0,
		memDeltaKB:      64,
		diskDeltaKB:     1024,
	}, nil)
	if err := ingestSvc.RegisterCollector(ctx, ingestsvc.RegisterCollectorRequest{
		CollectorName: "other-collector",
		Apps:          []string{"other-app"},
	}); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	user, err := authSvc.Register(ctx, authsvc.RegisterRequest{
		Username: "noaccess",
		Password: "secret123",
	})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	svc := New(mgr, ingestSvc, tracker, userSvc, &fakeConfigPusher{}, nil)
	return svc, user.ID
}

// --- Tests ---

func TestListCollectors_SingleCollector(t *testing.T) {
	svc, _, userID, _, _ := setupTestService(t)

	collectors, err := svc.ListCollectors(t.Context(), userID)
	if err != nil {
		t.Fatalf("ListCollectors: %v", err)
	}
	if len(collectors) != 1 {
		t.Fatalf("expected 1 collector (the registered one), got %d", len(collectors))
	}
	if collectors[0].Name != "test-collector" {
		t.Fatalf("expected collector 'test-collector', got %q", collectors[0].Name)
	}
}

func TestListCollectorApps_RespectsAccess(t *testing.T) {
	svc, panelDB, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	// Register a second app on the same collector without granting access.
	if err := svc.ingestSvc.RegisterCollector(ctx, ingestsvc.RegisterCollectorRequest{
		CollectorName: collectorName,
		Apps:          []string{"second-app"},
	}); err != nil {
		t.Fatalf("register second app: %v", err)
	}

	// The restricted user sees only the granted app.
	apps, err := svc.ListCollectorApps(ctx, ListCollectorAppsRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("ListCollectorApps: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 app (only the granted one), got %d: %v", len(apps), apps)
	}

	// An admin (no access policy rows) sees every app on disk.
	authSvc := authsvc.New(panelDB, testutil.TestKeySecret(), bcrypt.MinCost)
	admin, err := authSvc.Register(ctx, authsvc.RegisterRequest{
		Username: "admin2",
		Password: "secret123",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("register admin user: %v", err)
	}
	adminApps, err := svc.ListCollectorApps(ctx, ListCollectorAppsRequest{
		UserID:        admin.ID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("ListCollectorApps for admin: %v", err)
	}
	if len(adminApps) != 2 {
		t.Fatalf("expected admin to see 2 apps, got %d: %v", len(adminApps), adminApps)
	}
}

func TestListCollectors_AccessFiltering(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)

	collectors, err := svc.ListCollectors(t.Context(), noAccessUserID)
	if err != nil {
		t.Fatalf("ListCollectors: %v", err)
	}
	if len(collectors) != 0 {
		t.Fatalf("expected 0 collectors for user without access, got %d", len(collectors))
	}
}

func TestGetCollector_Found(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if c.Name != collectorName {
		t.Fatalf("expected name %q, got %q", collectorName, c.Name)
	}
}

func TestGetCollector_NotFound(t *testing.T) {
	svc, _, userID, _, _ := setupTestService(t)
	ctx := t.Context()

	_, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: "nonexistent",
	})
	if err != ErrCollectorNotFound {
		t.Fatalf("expected ErrCollectorNotFound, got %v", err)
	}
}

func TestGetCollector_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	_, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestGetCollector_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  GetCollectorRequest
	}{
		{name: "zero user ID", req: GetCollectorRequest{UserID: 0, CollectorName: "test"}},
		{name: "empty collector name", req: GetCollectorRequest{UserID: 1, CollectorName: ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGetCollector_ValidRequest(t *testing.T) {
	req := GetCollectorRequest{UserID: 1, CollectorName: "my-collector"}
	if err := req.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestListCollectorApps_Success(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	apps, err := svc.ListCollectorApps(ctx, ListCollectorAppsRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("ListCollectorApps: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 app, got %d: %v", len(apps), apps)
	}
	if apps[0] != appName {
		t.Fatalf("expected app %q, got %q", appName, apps[0])
	}
}

func TestListCollectorApps_NotFound(t *testing.T) {
	svc, _, userID, _, _ := setupTestService(t)
	ctx := t.Context()

	_, err := svc.ListCollectorApps(ctx, ListCollectorAppsRequest{
		UserID:        userID,
		CollectorName: "nonexistent",
	})
	if err != ErrCollectorNotFound {
		t.Fatalf("expected ErrCollectorNotFound, got %v", err)
	}
}

func TestListCollectorApps_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	_, err := svc.ListCollectorApps(ctx, ListCollectorAppsRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestListCollectorApps_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  ListCollectorAppsRequest
	}{
		{name: "zero user ID", req: ListCollectorAppsRequest{UserID: 0, CollectorName: "test"}},
		{name: "empty collector name", req: ListCollectorAppsRequest{UserID: 1, CollectorName: ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGetAppHealth_Success(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	health, err := svc.GetAppHealth(ctx, GetAppHealthRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		Since:         time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetAppHealth: %v", err)
	}
	if health.CurrentStatus != "" {
		t.Fatalf("expected empty health status (no data), got %q", health.CurrentStatus)
	}
}

func TestGetAppHealth_CheckCountsPerBucket(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()
	now := time.Now()

	for i := 0; i < 3; i++ {
		err := svc.ingestSvc.SaveHealthPeriod(ctx, ingestsvc.StoreHealthRequest{
			CollectorName: collectorName,
			AppName:       appName,
			Status:        types.HealthStatusUp,
			Time:          now.Add(-time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatalf("seed health check %d: %v", i, err)
		}
	}

	health, err := svc.GetAppHealth(ctx, GetAppHealthRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		Since:         now.Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("GetAppHealth: %v", err)
	}

	total := 0
	for _, b := range health.Buckets {
		if b.Checks < 0 {
			t.Fatalf("negative checks %d in bucket", b.Checks)
		}
		total += b.Checks
	}
	if total != 3 {
		t.Fatalf("expected 3 checks across buckets, got %d", total)
	}
}

func TestGetAppHealth_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	_, err := svc.GetAppHealth(ctx, GetAppHealthRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
		AppName:       "other-app",
		Since:         time.Now().Add(-1 * time.Hour),
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestGetAppHealth_AppNotFound(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	_, err := svc.GetAppHealth(ctx, GetAppHealthRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       "nonexistent-app",
		Since:         time.Now().Add(-1 * time.Hour),
	})
	// Access check happens before app existence check;
	// user does not have access to this app so we get access denied.
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestGetAppHealth_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  GetAppHealthRequest
	}{
		{name: "zero user ID", req: GetAppHealthRequest{UserID: 0, CollectorName: "c", AppName: "a", Since: time.Now()}},
		{name: "empty collector", req: GetAppHealthRequest{UserID: 1, CollectorName: "", AppName: "a", Since: time.Now()}},
		{name: "empty app", req: GetAppHealthRequest{UserID: 1, CollectorName: "c", AppName: "", Since: time.Now()}},
		{name: "since too old", req: GetAppHealthRequest{UserID: 1, CollectorName: "c", AppName: "a", Since: time.Now().Add(-31 * 24 * time.Hour)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestListCollectorAppsWithStats(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	if _, err := svc.ingestSvc.StoreLog(ctx, ingestsvc.StoreLogRequest{
		CollectorName: collectorName,
		AppName:       appName,
		Entry: types.LogEntry{
			Level:     types.LevelError,
			Message:   "boom",
			Timestamp: time.Now(),
		},
	}); err != nil {
		t.Fatalf("seed error log: %v", err)
	}
	if err := svc.ingestSvc.SaveHealthPeriod(ctx, ingestsvc.StoreHealthRequest{
		CollectorName: collectorName,
		AppName:       appName,
		Status:        types.HealthStatusUp,
		Time:          time.Now().Add(-30 * time.Minute),
	}); err != nil {
		t.Fatalf("seed health period: %v", err)
	}

	apps, err := svc.ListCollectorAppsWithStats(ctx, ListCollectorAppsWithStatsRequest{
		UserID:        userID,
		CollectorName: collectorName,
		Since:         time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("ListCollectorAppsWithStats: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(apps))
	}
	if apps[0].Name != appName {
		t.Fatalf("expected app %q, got %q", appName, apps[0].Name)
	}
	if apps[0].ErrorCount != 1 {
		t.Fatalf("expected 1 error, got %d", apps[0].ErrorCount)
	}
	if !apps[0].HasLogs {
		t.Fatal("expected app with seeded error log to have logs")
	}
	if apps[0].UptimePeriods < 1 {
		t.Fatalf("expected at least 1 uptime period, got %d", apps[0].UptimePeriods)
	}
	if apps[0].UptimePercent <= 0 || apps[0].UptimePercent > 100 {
		t.Fatalf("expected uptime percent in (0,100], got %f", apps[0].UptimePercent)
	}
}

func TestListCollectorAppsWithStats_EmptyApp(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	// A newly registered app has a DB but no data yet.
	if err := svc.ingestSvc.RegisterCollector(ctx, ingestsvc.RegisterCollectorRequest{
		CollectorName: collectorName,
		Apps:          []string{"empty-app"},
	}); err != nil {
		t.Fatalf("register empty app: %v", err)
	}
	// Grant access to empty-app so the access-filtered listing returns it.
	if err := svc.userSvc.GrantAccess(ctx, userID, usersvc.AppAccess{
		CollectorName: collectorName,
		AppName:       "empty-app",
	}); err != nil {
		t.Fatalf("grant access to empty app: %v", err)
	}

	apps, err := svc.ListCollectorAppsWithStats(ctx, ListCollectorAppsWithStatsRequest{
		UserID:        userID,
		CollectorName: collectorName,
		Since:         time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("ListCollectorAppsWithStats: %v", err)
	}
	for _, app := range apps {
		if app.Name == "empty-app" {
			if app.ErrorCount != 0 || app.UptimePeriods != 0 || app.HasLogs {
				t.Fatalf("expected empty app to degrade to N/A / 0, got errors=%d periods=%d hasLogs=%v", app.ErrorCount, app.UptimePeriods, app.HasLogs)
			}
			return
		}
	}
	t.Fatal("expected empty-app in results")
}

func TestCollectorActivity_NoStats(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	if _, ok, err := svc.CollectorActivity(ctx, collectorName); err != nil {
		t.Fatalf("CollectorActivity: %v", err)
	} else if ok {
		t.Fatal("expected no activity before any stats are saved")
	}
}

func TestCollectorActivity_WithStatsAndDefaultInterval(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 40.0,
		MemTotalMB:      8192,
		MemUsedMB:       2048,
		MemAvailableMB:  6144,
		DiskTotalGB:     250,
		DiskUsedGB:      100,
		DiskAvailGB:     150,
		Timestamp:       now,
	})

	act, ok, err := svc.CollectorActivity(ctx, collectorName)
	if err != nil {
		t.Fatalf("CollectorActivity: %v", err)
	}
	if !ok {
		t.Fatal("expected activity after saving stats")
	}
	if !act.At.Equal(now) {
		t.Fatalf("expected activity at %v, got %v", now, act.At)
	}
	if act.Interval != 30*time.Second {
		t.Fatalf("expected default 30s interval, got %v", act.Interval)
	}
}

func TestCollectorActivity_WithConfiguredInterval(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 40.0,
		MemTotalMB:      8192,
		MemUsedMB:       2048,
		MemAvailableMB:  6144,
		DiskTotalGB:     250,
		DiskUsedGB:      100,
		DiskAvailGB:     150,
		Timestamp:       now,
	})

	cfg := types.CollectorConfig{
		Stats: &types.StatsConfig{Interval: "300s", CPU: true, Mem: true, Disk: true},
	}
	cfgJSON, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := svc.SaveCollectorConfig(ctx, collectorName, string(cfgJSON)); err != nil {
		t.Fatalf("SaveCollectorConfig: %v", err)
	}

	act, ok, err := svc.CollectorActivity(ctx, collectorName)
	if err != nil {
		t.Fatalf("CollectorActivity: %v", err)
	}
	if !ok {
		t.Fatal("expected activity after saving stats")
	}
	if act.Interval != 5*time.Minute {
		t.Fatalf("expected 5m interval from config, got %v", act.Interval)
	}
}

func TestCollectorActivity_UnchangedStatsAdvanceTimestamp(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	t0 := time.Now().Truncate(time.Second)
	t1 := t0.Add(30 * time.Second)
	sample := types.Stats{
		CPUUsagePercent: 40.0,
		MemTotalMB:      8192,
		MemUsedMB:       2048,
		MemAvailableMB:  6144,
		DiskTotalGB:     250,
		DiskUsedGB:      100,
		DiskAvailGB:     150,
		Timestamp:       t0,
	}
	saveStats(t, ctx, svc, collectorName, sample)
	sample.Timestamp = t1
	saveStats(t, ctx, svc, collectorName, sample)

	act, ok, err := svc.CollectorActivity(ctx, collectorName)
	if err != nil {
		t.Fatalf("CollectorActivity: %v", err)
	}
	if !ok {
		t.Fatal("expected activity after saving stats")
	}
	if !act.At.Equal(t1) {
		t.Fatalf("expected activity at %v, got %v", t1, act.At)
	}
}

func TestEnsureSelfApps_GuessesMissingUnits(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	g := &recordingFormatGuesser{
		byService: map[string]types.FormatResultEvent{
			"tracer-collector": slogtextGuess("tracer-collector"),
			"tracer-panel":     slogtextGuess("tracer-panel"),
		},
	}
	svc.guesser = g

	if err := svc.EnsureSelfApps(t.Context(), collectorName); err != nil {
		t.Fatalf("EnsureSelfApps: %v", err)
	}
	if got := strings.Join(g.services, ","); got != "tracer-collector,tracer-panel" {
		t.Fatalf("guessed services = %q, want tracer-collector,tracer-panel", got)
	}

	raw, err := svc.FetchCollectorConfig(t.Context(), collectorName)
	if err != nil {
		t.Fatalf("FetchCollectorConfig: %v", err)
	}
	var cfg types.CollectorConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	if !collectorConfigHasApp(cfg, "tracer-collector") || !collectorConfigHasApp(cfg, "tracer-panel") {
		t.Fatalf("stored apps = %+v, want tracer-collector and tracer-panel", cfg.Apps)
	}
}

func TestEnsureSelfApps_SkipsPresentUnits(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()
	existing := types.CollectorConfig{}
	if err := existing.AddApp("tracer-collector", types.SourceSpec{
		Type:   "journalctl",
		Params: map[string]string{"unit": "tracer-collector.service"},
	}, types.ParserSpec{
		Type: "slogtext",
		Params: map[string]string{
			"level_field":      "level",
			"message_field":    "msg",
			"timestamp_field":  "time",
			"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
		},
	}); err != nil {
		t.Fatalf("seed tracer-collector: %v", err)
	}
	raw, err := json.Marshal(&existing)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := svc.SaveCollectorConfig(ctx, collectorName, string(raw)); err != nil {
		t.Fatalf("SaveCollectorConfig: %v", err)
	}

	g := &recordingFormatGuesser{
		byService: map[string]types.FormatResultEvent{
			"tracer-panel": slogtextGuess("tracer-panel"),
		},
	}
	svc.guesser = g
	if err := svc.EnsureSelfApps(ctx, collectorName); err != nil {
		t.Fatalf("EnsureSelfApps: %v", err)
	}
	if got := strings.Join(g.services, ","); got != "tracer-panel" {
		t.Fatalf("guessed services = %q, want tracer-panel only", got)
	}
}

func TestEnsureSelfApps_NotFoundIsSkipped(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	svc.guesser = &recordingFormatGuesser{}
	if err := svc.EnsureSelfApps(t.Context(), collectorName); err != nil {
		t.Fatalf("EnsureSelfApps: %v", err)
	}
	raw, err := svc.FetchCollectorConfig(t.Context(), collectorName)
	if err != nil {
		t.Fatalf("FetchCollectorConfig: %v", err)
	}
	if raw != "" {
		t.Fatalf("expected no stored config when guesses are not found, got %s", raw)
	}
}

func TestEnsureSelfApps_NilGuesser(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	if err := svc.EnsureSelfApps(t.Context(), collectorName); err != nil {
		t.Fatalf("EnsureSelfApps with nil guesser: %v", err)
	}
}

func TestAppActivity_NoData(t *testing.T) {
	svc, _, _, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	if _, ok, err := svc.AppActivity(ctx, collectorName, appName); err != nil {
		t.Fatalf("AppActivity: %v", err)
	} else if ok {
		t.Fatal("expected no activity before any data is stored")
	}
}

func TestAppActivity_WithLog(t *testing.T) {
	svc, _, _, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	if _, err := svc.ingestSvc.StoreLog(ctx, ingestsvc.StoreLogRequest{
		CollectorName: collectorName,
		AppName:       appName,
		Entry: types.LogEntry{
			Level:     types.LevelInfo,
			Message:   "hello",
			Timestamp: now,
		},
	}); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	act, ok, err := svc.AppActivity(ctx, collectorName, appName)
	if err != nil {
		t.Fatalf("AppActivity: %v", err)
	}
	if !ok {
		t.Fatal("expected activity after storing a log")
	}
	if act.At.Unix() != now.Unix() {
		t.Fatalf("expected activity at %d, got %d", now.Unix(), act.At.Unix())
	}
	if act.Interval != 30*time.Second {
		t.Fatalf("expected default 30s health interval, got %v", act.Interval)
	}
}

func TestAppActivity_HealthCheckBeatsLog(t *testing.T) {
	svc, _, _, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	logTime := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	hcTime := time.Now().Add(-30 * time.Minute).Truncate(time.Second)

	if _, err := svc.ingestSvc.StoreLog(ctx, ingestsvc.StoreLogRequest{
		CollectorName: collectorName,
		AppName:       appName,
		Entry: types.LogEntry{
			Level:     types.LevelInfo,
			Message:   "hello",
			Timestamp: logTime,
		},
	}); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	if err := svc.ingestSvc.SaveHealthPeriod(ctx, ingestsvc.StoreHealthRequest{
		CollectorName: collectorName,
		AppName:       appName,
		Status:        types.HealthStatusUp,
		Time:          hcTime,
	}); err != nil {
		t.Fatalf("seed health check: %v", err)
	}

	act, ok, err := svc.AppActivity(ctx, collectorName, appName)
	if err != nil {
		t.Fatalf("AppActivity: %v", err)
	}
	if !ok {
		t.Fatal("expected activity")
	}
	if act.At.Unix() != hcTime.Unix() {
		t.Fatalf("expected activity at health check time %d, got %d", hcTime.Unix(), act.At.Unix())
	}
}

func TestListCollectorAppsWithStats_PopulatesLastUpdate(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	if _, err := svc.ingestSvc.StoreLog(ctx, ingestsvc.StoreLogRequest{
		CollectorName: collectorName,
		AppName:       appName,
		Entry: types.LogEntry{
			Level:     types.LevelInfo,
			Message:   "hello",
			Timestamp: time.Now().Add(-5 * time.Minute),
		},
	}); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	apps, err := svc.ListCollectorAppsWithStats(ctx, ListCollectorAppsWithStatsRequest{
		UserID:        userID,
		CollectorName: collectorName,
		Since:         time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("ListCollectorAppsWithStats: %v", err)
	}
	if len(apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(apps))
	}
	if apps[0].LastUpdateAt.IsZero() {
		t.Fatal("expected LastUpdateAt to be populated")
	}
	if apps[0].Interval != 30*time.Second {
		t.Fatalf("expected default 30s interval, got %v", apps[0].Interval)
	}
}

func TestListCollectorAppsWithStats_NotFound(t *testing.T) {
	svc, _, userID, _, _ := setupTestService(t)
	ctx := t.Context()

	_, err := svc.ListCollectorAppsWithStats(ctx, ListCollectorAppsWithStatsRequest{
		UserID:        userID,
		CollectorName: "ghost",
		Since:         time.Now().Add(-time.Hour),
	})
	if err != ErrCollectorNotFound {
		t.Fatalf("expected ErrCollectorNotFound, got %v", err)
	}
}

func TestListCollectorAppsWithStats_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	_, err := svc.ListCollectorAppsWithStats(ctx, ListCollectorAppsWithStatsRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
		Since:         time.Now().Add(-time.Hour),
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestListCollectorAppsWithStats_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  ListCollectorAppsWithStatsRequest
	}{
		{name: "zero user ID", req: ListCollectorAppsWithStatsRequest{UserID: 0, CollectorName: "c", Since: time.Now()}},
		{name: "empty collector", req: ListCollectorAppsWithStatsRequest{UserID: 1, CollectorName: "", Since: time.Now()}},
		{name: "since too old", req: ListCollectorAppsWithStatsRequest{UserID: 1, CollectorName: "c", Since: time.Now().Add(-31 * 24 * time.Hour)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGetStatsHistory_Success(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	// Insert some stats via the ingest service
	ingestSvc := svc.ingestSvc
	if err := ingestSvc.SaveSystemStats(ctx, ingestsvc.SaveSystemStatsRequest{
		CollectorName: collectorName,
		Stats: types.Stats{
			CPUUsagePercent: 42.5,
			MemTotalMB:      16384,
			MemUsedMB:       8192,
			MemAvailableMB:  8192,
			DiskTotalGB:     500,
			DiskUsedGB:      250,
			DiskAvailGB:     250,
			Timestamp:       time.Now().Add(-5 * time.Minute),
		},
	}); err != nil {
		t.Fatalf("SaveSystemStats: %v", err)
	}
	_ = appName

	result, err := svc.GetStatsHistory(ctx, GetStatsHistoryRequest{
		UserID:        userID,
		CollectorName: collectorName,
		Since:         time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetStatsHistory: %v", err)
	}
	if len(result.CPUPeriods) != 1 {
		t.Fatalf("expected 1 cpu period, got %d", len(result.CPUPeriods))
	}
	if result.CPUPeriods[0].UsagePercent != 42.5 {
		t.Fatalf("expected CPU 42.5, got %f", result.CPUPeriods[0].UsagePercent)
	}
	if len(result.MemPeriods) != 1 {
		t.Fatalf("expected 1 mem period, got %d", len(result.MemPeriods))
	}
	if len(result.DiskPeriods) != 1 {
		t.Fatalf("expected 1 disk period, got %d", len(result.DiskPeriods))
	}
}

func TestGetStatsHistory_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	_, err := svc.GetStatsHistory(ctx, GetStatsHistoryRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
		Since:         time.Now().Add(-1 * time.Hour),
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestGetStatsHistory_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  GetStatsHistoryRequest
	}{
		{name: "zero user ID", req: GetStatsHistoryRequest{UserID: 0, CollectorName: "c", Since: time.Now()}},
		{name: "empty collector", req: GetStatsHistoryRequest{UserID: 1, CollectorName: "", Since: time.Now()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGetAppStats_Success(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	stats, err := svc.GetAppStats(ctx, GetAppStatsRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		Since:         time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetAppStats: %v", err)
	}
	if stats.Total != 0 {
		t.Fatalf("expected total 0, got %d", stats.Total)
	}
	if stats.HasLogs {
		t.Fatal("expected fresh app to have no logs")
	}
}

func TestGetAppStats_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	_, err := svc.GetAppStats(ctx, GetAppStatsRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
		AppName:       "other-app",
		Since:         time.Now().Add(-1 * time.Hour),
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestGetAppStats_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  GetAppStatsRequest
	}{
		{name: "zero user ID", req: GetAppStatsRequest{UserID: 0, CollectorName: "c", AppName: "a", Since: time.Now()}},
		{name: "empty collector", req: GetAppStatsRequest{UserID: 1, CollectorName: "", AppName: "a", Since: time.Now()}},
		{name: "empty app", req: GetAppStatsRequest{UserID: 1, CollectorName: "c", AppName: "", Since: time.Now()}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGetLogGroups_Success(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	groups, err := svc.GetLogGroups(ctx, GetLogGroupsRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		Level:         "all",
		Since:         time.Now().Add(-1 * time.Hour),
		Offset:        0,
		Limit:         50,
	})
	if err != nil {
		t.Fatalf("GetLogGroups: %v", err)
	}
	if groups == nil {
		t.Fatal("expected non-nil groups slice")
	}
	if len(groups) != 0 {
		t.Fatalf("expected 0 groups, got %d", len(groups))
	}
}

func TestGetLogGroups_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	_, err := svc.GetLogGroups(ctx, GetLogGroupsRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
		AppName:       "other-app",
		Level:         "all",
		Since:         time.Now().Add(-1 * time.Hour),
		Offset:        0,
		Limit:         50,
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestGetLogGroups_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  GetLogGroupsRequest
	}{
		{name: "zero user ID", req: GetLogGroupsRequest{UserID: 0, CollectorName: "c", AppName: "a", Level: "all", Since: time.Now(), Offset: 0, Limit: 50}},
		{name: "empty collector", req: GetLogGroupsRequest{UserID: 1, CollectorName: "", AppName: "a", Level: "all", Since: time.Now(), Offset: 0, Limit: 50}},
		{name: "empty app", req: GetLogGroupsRequest{UserID: 1, CollectorName: "c", AppName: "", Level: "all", Since: time.Now(), Offset: 0, Limit: 50}},
		{name: "negative offset", req: GetLogGroupsRequest{UserID: 1, CollectorName: "c", AppName: "a", Level: "all", Since: time.Now(), Offset: -1, Limit: 50}},
		{name: "zero limit", req: GetLogGroupsRequest{UserID: 1, CollectorName: "c", AppName: "a", Level: "all", Since: time.Now(), Offset: 0, Limit: 0}},
		{name: "negative limit", req: GetLogGroupsRequest{UserID: 1, CollectorName: "c", AppName: "a", Level: "all", Since: time.Now(), Offset: 0, Limit: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGetLogGroup_NotFound(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	_, err := svc.GetLogGroup(ctx, GetLogGroupRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		GroupID:       999,
	})
	if err != ErrLogNotFound {
		t.Fatalf("expected ErrLogNotFound, got %v", err)
	}
}

func TestGetLogGroupAndSummary(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	// Create the log group through the public ingest API.
	if _, err := svc.ingestSvc.StoreLog(ctx, ingestsvc.StoreLogRequest{
		CollectorName: collectorName,
		AppName:       appName,
		Entry: types.LogEntry{
			Level:     types.LevelError,
			Message:   "boom",
			Timestamp: time.Now(),
		},
	}); err != nil {
		t.Fatalf("store log: %v", err)
	}

	group, err := svc.GetLogGroup(ctx, GetLogGroupRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		GroupID:       1,
	})
	if err != nil {
		t.Fatalf("GetLogGroup: %v", err)
	}
	if group.Message != "boom" || group.Level != string(types.LevelError) || group.Count != 1 {
		t.Errorf("unexpected log group: %+v", group)
	}

	summary, err := svc.GetLogGroupSummary(ctx, GetLogGroupSummaryRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		GroupID:       1,
		Since:         0,
	})
	if err != nil {
		t.Fatalf("GetLogGroupSummary: %v", err)
	}
	if summary.Message != "boom" || summary.Count != 1 {
		t.Errorf("unexpected summary: %+v", summary)
	}
}

func TestGetLogGroup_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	_, err := svc.GetLogGroup(ctx, GetLogGroupRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
		AppName:       "other-app",
		GroupID:       1,
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestGetLogGroup_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  GetLogGroupRequest
	}{
		{name: "zero user ID", req: GetLogGroupRequest{UserID: 0, CollectorName: "c", AppName: "a", GroupID: 1}},
		{name: "empty collector", req: GetLogGroupRequest{UserID: 1, CollectorName: "", AppName: "a", GroupID: 1}},
		{name: "empty app", req: GetLogGroupRequest{UserID: 1, CollectorName: "c", AppName: "", GroupID: 1}},
		{name: "zero group ID", req: GetLogGroupRequest{UserID: 1, CollectorName: "c", AppName: "a", GroupID: 0}},
		{name: "negative group ID", req: GetLogGroupRequest{UserID: 1, CollectorName: "c", AppName: "a", GroupID: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestListGroupLogs_Empty(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	entries, err := svc.ListGroupLogs(ctx, ListGroupLogsRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		GroupID:       1,
		Since:         time.Now().Add(-1 * time.Hour),
		Offset:        0,
		Limit:         50,
	})
	if err != nil {
		t.Fatalf("ListGroupLogs: %v", err)
	}
	if entries == nil {
		t.Fatal("expected non-nil entries slice")
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestListGroupLogs_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	_, err := svc.ListGroupLogs(ctx, ListGroupLogsRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
		AppName:       "other-app",
		GroupID:       1,
		Since:         time.Now().Add(-1 * time.Hour),
		Offset:        0,
		Limit:         50,
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestListGroupLogs_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  ListGroupLogsRequest
	}{
		{name: "zero user ID", req: ListGroupLogsRequest{UserID: 0, CollectorName: "c", AppName: "a", GroupID: 1, Since: time.Now(), Offset: 0, Limit: 50}},
		{name: "empty collector", req: ListGroupLogsRequest{UserID: 1, CollectorName: "", AppName: "a", GroupID: 1, Since: time.Now(), Offset: 0, Limit: 50}},
		{name: "empty app", req: ListGroupLogsRequest{UserID: 1, CollectorName: "c", AppName: "", GroupID: 1, Since: time.Now(), Offset: 0, Limit: 50}},
		{name: "zero group ID", req: ListGroupLogsRequest{UserID: 1, CollectorName: "c", AppName: "a", GroupID: 0, Since: time.Now(), Offset: 0, Limit: 50}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGetLogByID_NotFound(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	_, err := svc.GetLogByID(ctx, GetLogByIDRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		ID:            999,
	})
	if err != ErrLogNotFound {
		t.Fatalf("expected ErrLogNotFound, got %v", err)
	}
}

func TestGetLogByID_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	_, err := svc.GetLogByID(ctx, GetLogByIDRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
		AppName:       "other-app",
		ID:            1,
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestGetLogByID_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  GetLogByIDRequest
	}{
		{name: "zero user ID", req: GetLogByIDRequest{UserID: 0, CollectorName: "c", AppName: "a", ID: 1}},
		{name: "empty collector", req: GetLogByIDRequest{UserID: 1, CollectorName: "", AppName: "a", ID: 1}},
		{name: "empty app", req: GetLogByIDRequest{UserID: 1, CollectorName: "c", AppName: "", ID: 1}},
		{name: "zero ID", req: GetLogByIDRequest{UserID: 1, CollectorName: "c", AppName: "a", ID: 0}},
		{name: "negative ID", req: GetLogByIDRequest{UserID: 1, CollectorName: "c", AppName: "a", ID: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDeleteLogGroup_NotFound(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	err := svc.DeleteLogGroup(ctx, DeleteLogGroupRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		GroupID:       999,
	})
	if err != ErrLogNotFound {
		t.Fatalf("expected ErrLogNotFound, got %v", err)
	}
}

func TestDeleteLogGroup_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	err := svc.DeleteLogGroup(ctx, DeleteLogGroupRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
		AppName:       "other-app",
		GroupID:       1,
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestDeleteLogGroup_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  DeleteLogGroupRequest
	}{
		{name: "zero user ID", req: DeleteLogGroupRequest{UserID: 0, CollectorName: "c", AppName: "a", GroupID: 1}},
		{name: "empty collector", req: DeleteLogGroupRequest{UserID: 1, CollectorName: "", AppName: "a", GroupID: 1}},
		{name: "empty app", req: DeleteLogGroupRequest{UserID: 1, CollectorName: "c", AppName: "", GroupID: 1}},
		{name: "zero group ID", req: DeleteLogGroupRequest{UserID: 1, CollectorName: "c", AppName: "a", GroupID: 0}},
		{name: "negative group ID", req: DeleteLogGroupRequest{UserID: 1, CollectorName: "c", AppName: "a", GroupID: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.req.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestGetLogVolumeTimeSeries_Success(t *testing.T) {
	svc, _, _, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	rows, err := svc.GetLogVolumeTimeSeries(ctx, collectorName, appName, time.Now().Add(-1*time.Hour), "hour")
	if err != nil {
		t.Fatalf("GetLogVolumeTimeSeries: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows for empty data, got %d", len(rows))
	}
}

func TestGetLogVolumeTimeSeries_AppNotFound(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	_, err := svc.GetLogVolumeTimeSeries(ctx, collectorName, "nonexistent", time.Now().Add(-1*time.Hour), "hour")
	if err != ErrAppNotFound {
		t.Fatalf("expected ErrAppNotFound, got %v", err)
	}
}

func TestSinceWithinRange(t *testing.T) {
	tests := []struct {
		name  string
		since time.Time
		want  bool
	}{
		{name: "now", since: time.Now(), want: true},
		{name: "1 hour ago", since: time.Now().Add(-1 * time.Hour), want: true},
		{name: "29 days ago", since: time.Now().Add(-29 * 24 * time.Hour), want: true},
		{name: "30 days ago", since: time.Now().Add(-30 * 24 * time.Hour), want: true},
		{name: "31 days ago", since: time.Now().Add(-31 * 24 * time.Hour), want: false},
		{name: "future", since: time.Now().Add(1 * time.Hour), want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sinceWithinRange(tt.since)
			if got != tt.want {
				t.Fatalf("sinceWithinRange(%v) = %v, want %v", tt.since, got, tt.want)
			}
		})
	}
}

// TestCollectorStats_Staleness_Fresh verifies staleness="fresh" for a recent stat
// using the public GetCollector API.
func TestCollectorStats_Staleness_Fresh(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 50.0,
		MemTotalMB:      16384,
		MemUsedMB:       4096,
		MemAvailableMB:  12288,
		DiskTotalGB:     500,
		DiskUsedGB:      100,
		DiskAvailGB:     400,
		Timestamp:       time.Now(),
	})

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if c.Stats == nil {
		t.Fatal("expected non-nil Stats")
	}
	if c.Stats.CPUUsagePercent != 50.0 {
		t.Fatalf("CPU: expected 50.0, got %f", c.Stats.CPUUsagePercent)
	}
	if c.Stats.MemTotalMB != 16384 {
		t.Fatalf("MemTotalMB: expected 16384, got %d", c.Stats.MemTotalMB)
	}
	if c.Stats.Staleness != "fresh" {
		t.Fatalf("expected fresh staleness, got %q", c.Stats.Staleness)
	}
}

// TestCollectorStats_Staleness_Stale verifies staleness="stale" for a
// 45-min-old stat via the public GetCollector API.
func TestCollectorStats_Staleness_Stale(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	ts := time.Now().Add(-45 * time.Minute)
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 10.0,
		MemTotalMB:      4096,
		MemUsedMB:       1024,
		MemAvailableMB:  3072,
		DiskTotalGB:     100,
		DiskUsedGB:      50,
		DiskAvailGB:     50,
		Timestamp:       ts,
	})

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if want := wantStaleness(ts); c.Stats.Staleness != want {
		t.Fatalf("expected %q staleness, got %q", want, c.Stats.Staleness)
	}
}

// TestCollectorStats_Staleness_Warning verifies staleness="warning" for a
// 7-min-old stat via the public GetCollector API.
func TestCollectorStats_Staleness_Warning(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	ts := time.Now().Add(-7 * time.Minute)
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 10.0,
		MemTotalMB:      4096,
		MemUsedMB:       1024,
		MemAvailableMB:  3072,
		DiskTotalGB:     100,
		DiskUsedGB:      50,
		DiskAvailGB:     50,
		Timestamp:       ts,
	})

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if want := wantStaleness(ts); c.Stats.Staleness != want {
		t.Fatalf("expected %q staleness, got %q", want, c.Stats.Staleness)
	}
}

// TestCollectorStats_Staleness_Old verifies staleness="old" for a 24h-old stat.
func TestCollectorStats_Staleness_Old(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 10.0,
		MemTotalMB:      4096,
		MemUsedMB:       1024,
		MemAvailableMB:  3072,
		DiskTotalGB:     100,
		DiskUsedGB:      50,
		DiskAvailGB:     50,
		Timestamp:       time.Now().Add(-24 * time.Hour),
	})

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if c.Stats.Staleness != "old" {
		t.Fatalf("expected old, got %q", c.Stats.Staleness)
	}
}

// TestFormatCollectorStats_StalenessBuckets verifies the staleness buckets
// deterministically with a fixed reference time. The integration tests above
// go through the public API and therefore depend on the wall clock; this test
// pins the actual bucket boundaries.
func TestFormatCollectorStats_StalenessBuckets(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		age  time.Duration
		want string
	}{
		{name: "fresh", age: time.Minute, want: "fresh"},
		{name: "warning", age: 7 * time.Minute, want: "warning"},
		{name: "stale", age: 45 * time.Minute, want: "stale"},
		{name: "old same time yesterday", age: 24 * time.Hour, want: "old"},
		{name: "old across midnight", age: 25 * time.Hour, want: "old"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := ingestsvc.SystemStat{Timestamp: now.Add(-tt.age).Unix()}
			got := formatCollectorStats(stats, now)
			if got.Staleness != tt.want {
				t.Fatalf("staleness for age %v = %q, want %q", tt.age, got.Staleness, tt.want)
			}
		})
	}
}

// wantStaleness mirrors the staleness rules in formatCollectorStats so the
// integration assertions above are independent of the wall clock: a stat 45
// minutes old must not flip from "stale" to "old" merely because the test runs
// shortly after midnight.
func wantStaleness(ts time.Time) string {
	now := time.Now()
	age := now.Sub(ts)
	sameDate := now.Year() == ts.Year() && now.YearDay() == ts.YearDay()
	switch {
	case !sameDate:
		return "old"
	case age > 30*time.Minute:
		return "stale"
	case age > 5*time.Minute:
		return "warning"
	default:
		return "fresh"
	}
}

func TestOverlapDuration(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	period := db.AppHealthPeriod{
		StartedAt: now.Add(-2 * time.Hour).Unix(),
		EndedAt:   sql.NullInt64{Int64: now.Add(-1 * time.Hour).Unix(), Valid: true},
		Status:    "up",
	}

	// Query window: last 3 hours
	queryStart := now.Add(-3 * time.Hour)
	queryEnd := now

	dur := overlapDuration(period, queryStart, queryEnd)
	if dur <= 0 {
		t.Fatalf("expected positive overlap duration, got %v", dur)
	}
	// Period is 2h ago to 1h ago, query is 3h ago to now
	// Overlap: 2h ago to 1h ago = 1 hour
	expected := 1 * time.Hour
	if dur != expected {
		t.Fatalf("expected overlap %v, got %v", expected, dur)
	}
}

func TestOverlapDuration_NoOverlap(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	period := db.AppHealthPeriod{
		StartedAt: now.Add(-4 * time.Hour).Unix(),
		EndedAt:   sql.NullInt64{Int64: now.Add(-3 * time.Hour).Unix(), Valid: true},
		Status:    "up",
	}

	queryStart := now.Add(-2 * time.Hour)
	queryEnd := now

	dur := overlapDuration(period, queryStart, queryEnd)
	if dur != 0 {
		t.Fatalf("expected 0 overlap, got %v", dur)
	}
}

func TestOverlapDuration_OpenEnded(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	period := db.AppHealthPeriod{
		StartedAt: now.Add(-1 * time.Hour).Unix(),
		EndedAt:   sql.NullInt64{Valid: false}, // open-ended
		Status:    "up",
	}

	queryStart := now.Add(-2 * time.Hour)
	queryEnd := now

	dur := overlapDuration(period, queryStart, queryEnd)
	if dur <= 0 {
		t.Fatalf("expected positive overlap for open-ended period, got %v", dur)
	}
}

func TestOverlapDuration_ReversedPeriod(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	// Period with end before start (invalid data)
	period := db.AppHealthPeriod{
		StartedAt: now.Add(-1 * time.Hour).Unix(),
		EndedAt:   sql.NullInt64{Int64: now.Add(-2 * time.Hour).Unix(), Valid: true},
		Status:    "up",
	}

	queryStart := now.Add(-3 * time.Hour)
	queryEnd := now

	dur := overlapDuration(period, queryStart, queryEnd)
	// Should handle gracefully - end is clamped to start
	if dur < 0 {
		t.Fatalf("expected non-negative overlap, got %v", dur)
	}
}

func TestMinTime(t *testing.T) {
	a := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	b := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	if got := minTime(a, b); !got.Equal(a) {
		t.Fatalf("expected %v, got %v", a, got)
	}
	if got := minTime(b, a); !got.Equal(a) {
		t.Fatalf("expected %v, got %v", a, got)
	}
	if got := minTime(a, a); !got.Equal(a) {
		t.Fatalf("expected %v, got %v", a, got)
	}
}

func TestMaxTime(t *testing.T) {
	a := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	b := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	if got := maxTime(a, b); !got.Equal(b) {
		t.Fatalf("expected %v, got %v", b, got)
	}
	if got := maxTime(b, a); !got.Equal(b) {
		t.Fatalf("expected %v, got %v", b, got)
	}
	if got := maxTime(a, a); !got.Equal(a) {
		t.Fatalf("expected %v, got %v", a, got)
	}
}

func TestBuildHealthBuckets(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	since := now.Add(-3 * time.Hour)

	periods := []db.AppHealthPeriod{
		{
			StartedAt: now.Add(-3 * time.Hour).Unix(),
			EndedAt:   sql.NullInt64{Int64: now.Add(-2 * time.Hour).Unix(), Valid: true},
			Status:    "up",
		},
		{
			StartedAt: now.Add(-2 * time.Hour).Unix(),
			EndedAt:   sql.NullInt64{Int64: now.Add(-1 * time.Hour).Unix(), Valid: true},
			Status:    "down",
		},
	}

	buckets := buildHealthBuckets(periods, nil, since, now)
	if len(buckets) < 37 {
		t.Fatalf("expected at least 37 buckets, got %d", len(buckets))
	}

	// Verify bucket boundaries
	for _, b := range buckets {
		if b.End.Before(b.Start) {
			t.Fatalf("bucket end %v before start %v", b.End, b.Start)
		}
	}

	// Verify durations are non-negative
	totalUp := time.Duration(0)
	totalDown := time.Duration(0)
	for _, b := range buckets {
		totalUp += b.UpDuration
		totalDown += b.DownDuration
	}
	if totalUp+totalDown <= 0 {
		t.Fatal("expected some duration in buckets")
	}
}

func TestBuildHealthBucketsCheckCounts(t *testing.T) {
	now := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	since := now.Add(-3 * time.Hour)
	bucketCount := 37
	slotSize := int64(now.Sub(since)/time.Second) / int64(bucketCount)

	checkCounts := []db.ListHealthCheckCountsBySlotRow{
		{Slot: 0, Cnt: 2},
		{Slot: 5, Cnt: 3},
		{Slot: int64(bucketCount), Cnt: 1},
	}

	buckets := buildHealthBuckets(nil, checkCounts, since, now)
	if len(buckets) != bucketCount {
		t.Fatalf("expected %d buckets, got %d", bucketCount, len(buckets))
	}
	if got := buckets[0].Checks; got != 2 {
		t.Fatalf("expected 2 checks in bucket 0, got %d", got)
	}
	if got := buckets[5].Checks; got != 3 {
		t.Fatalf("expected 3 checks in bucket 5, got %d", got)
	}
	if got := buckets[bucketCount-1].Checks; got != 1 {
		t.Fatalf("expected tail slot clamped to last bucket with 1 check, got %d", got)
	}
	if got := buckets[0].Start; !got.Equal(since) {
		t.Fatalf("expected first bucket to start at since, got %v", got)
	}
	expectedLastStart := since.Add(time.Duration(bucketCount-1) * time.Duration(slotSize) * time.Second)
	if got := buckets[bucketCount-1].Start; !got.Equal(expectedLastStart) {
		t.Fatalf("expected last bucket to start at %v, got %v", expectedLastStart, got)
	}
}

// --- Edge cases ---

func TestGetCollector_WithStats(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	// Insert stats
	if err := svc.ingestSvc.SaveSystemStats(ctx, ingestsvc.SaveSystemStatsRequest{
		CollectorName: collectorName,
		Stats: types.Stats{
			CPUUsagePercent: 75.0,
			MemTotalMB:      16384,
			MemUsedMB:       8192,
			MemAvailableMB:  8192,
			DiskTotalGB:     500,
			DiskUsedGB:      100,
			DiskAvailGB:     400,
			Timestamp:       time.Now().Add(-2 * time.Minute),
		},
	}); err != nil {
		t.Fatalf("SaveSystemStats: %v", err)
	}

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if c.Stats == nil {
		t.Fatal("expected collector stats to be present")
	}
	if c.Stats.CPUUsagePercent != 75.0 {
		t.Fatalf("expected CPU 75.0, got %f", c.Stats.CPUUsagePercent)
	}
}

func TestGetCollector_WithTrackerData(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	// Register the collector in the tracker
	svc.tracker.Register("conn-1", collectorName, "192.168.1.1:1234", []string{"test-app"})

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if !c.IsConnected {
		t.Fatal("expected collector to be connected")
	}
	if c.RemoteAddr != "192.168.1.1:1234" {
		t.Fatalf("expected remote addr '192.168.1.1:1234', got %q", c.RemoteAddr)
	}
}

func TestGetCollector_Disconnected(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	// Register then disconnect
	svc.tracker.Register("conn-1", collectorName, "192.168.1.1:1234", []string{"test-app"})
	svc.tracker.Disconnect("conn-1")

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if c.IsConnected {
		t.Fatal("expected collector to be disconnected")
	}
}

func TestListCollectorApps_AppFiltering(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	// Register a second app for the same collector
	mgr := svc.dbManager
	if _, err := mgr.CreateAppDB(collectorName, "another-app"); err != nil {
		t.Fatalf("create second app: %v", err)
	}

	// User only has access to "test-app" (setupTestService granted access to test-app)
	apps, err := svc.ListCollectorApps(ctx, ListCollectorAppsRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("ListCollectorApps: %v", err)
	}
	// User should only see "test-app" since access was granted only to that
	if len(apps) != 1 || apps[0] != "test-app" {
		t.Fatalf("expected [test-app], got %v", apps)
	}
}

func TestReadAppHealth_OpenPeriod(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	// Insert a health period via the ingest service
	if err := svc.ingestSvc.SaveHealthPeriod(ctx, ingestsvc.StoreHealthRequest{
		CollectorName: collectorName,
		AppName:       appName,
		Status:        types.HealthStatusUp,
		Time:          time.Now().Add(-30 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveHealthPeriod: %v", err)
	}

	health, err := svc.GetAppHealth(ctx, GetAppHealthRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		Since:         time.Now().Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetAppHealth: %v", err)
	}
	if health.CurrentStatus != types.HealthStatusUp {
		t.Fatalf("expected current status 'up', got %q", health.CurrentStatus)
	}
	if health.PeriodCount < 1 {
		t.Fatalf("expected at least 1 health period, got %d", health.PeriodCount)
	}
}

func TestGetLogGroups_LevelFiltering(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	// Test with specific level that has no data
	groups, err := svc.GetLogGroups(ctx, GetLogGroupsRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		Level:         "error",
		Since:         time.Now().Add(-1 * time.Hour),
		Offset:        0,
		Limit:         50,
	})
	if err != nil {
		t.Fatalf("GetLogGroups with level filter: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("expected 0 error groups, got %d", len(groups))
	}
}

func TestGetLogVolumeTimeSeries_EmptyResult(t *testing.T) {
	svc, _, _, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	rows, err := svc.GetLogVolumeTimeSeries(ctx, collectorName, appName, time.Now().Add(-1*time.Hour), "hour")
	if err != nil {
		t.Fatalf("GetLogVolumeTimeSeries: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows for empty data, got %d", len(rows))
	}
}

func TestValidateCollectorNameEmpty(t *testing.T) {
	err := domain.ValidateCollectorName("")
	if err == nil {
		t.Fatal("expected error for empty collector name")
	}
}

func TestValidateAppNameEmpty(t *testing.T) {
	err := domain.ValidateAppName("")
	if err == nil {
		t.Fatal("expected error for empty app name")
	}
}

// Test that dbmanager path for tests works correctly
func TestDBManagerCreatesDirectories(t *testing.T) {
	dir := t.TempDir()
	mgr, err := dbmanager.New(dir)
	if err != nil {
		t.Fatalf("new dbmanager: %v", err)
	}

	_, err = mgr.CreateCollectorDB("test-collector")
	if err != nil {
		t.Fatalf("create collector db: %v", err)
	}

	collectors, err := mgr.ListCollectors()
	if err != nil {
		t.Fatalf("list collectors: %v", err)
	}
	if len(collectors) != 1 || collectors[0] != "test-collector" {
		t.Fatalf("expected [test-collector], got %v", collectors)
	}

	// Verify directory was created
	collectorDir := filepath.Join(dir, "collectors", "test-collector")
	if _, err := os.Stat(collectorDir); os.IsNotExist(err) {
		t.Fatalf("collector directory was not created at %s", collectorDir)
	}
}

func TestDBManager_CreateAppDB(t *testing.T) {
	dir := t.TempDir()
	mgr, err := dbmanager.New(dir)
	if err != nil {
		t.Fatalf("new dbmanager: %v", err)
	}

	_, err = mgr.CreateCollectorDB("my-collector")
	if err != nil {
		t.Fatalf("create collector db: %v", err)
	}

	_, err = mgr.CreateAppDB("my-collector", "my-app")
	if err != nil {
		t.Fatalf("create app db: %v", err)
	}

	apps, err := mgr.ListApps("my-collector")
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	if len(apps) != 1 || apps[0] != "my-app" {
		t.Fatalf("expected [my-app], got %v", apps)
	}
}

func TestDBManager_DeleteAppDB(t *testing.T) {
	mgr := testutil.NewTestDBManager(t)

	if _, err := mgr.CreateAppDB("my-collector", "my-app"); err != nil {
		t.Fatalf("create app db: %v", err)
	}
	if _, err := mgr.CreateAppDB("my-collector", "keep-app"); err != nil {
		t.Fatalf("create keep-app db: %v", err)
	}

	if err := mgr.DeleteAppDB("my-collector", "my-app"); err != nil {
		t.Fatalf("delete app db: %v", err)
	}

	apps, err := mgr.ListApps("my-collector")
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	if len(apps) != 1 || apps[0] != "keep-app" {
		t.Fatalf("expected [keep-app] after delete, got %v", apps)
	}

	handle, err := mgr.GetAppDB("my-collector", "my-app")
	if !errors.Is(err, dbmanager.ErrDBNotFound) {
		t.Fatalf("expected ErrDBNotFound after delete, got handle=%v err=%v", handle, err)
	}

	if err := mgr.DeleteAppDB("my-collector", "missing-app"); err != nil {
		t.Fatalf("deleting a never-created app db should be idempotent: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Resource monitoring integration tests
// ---------------------------------------------------------------------------

// saveStats is a helper to call SaveSystemStats via the ingest service.
func saveStats(t *testing.T, ctx context.Context, svc *Service, collectorName string, stats types.Stats) {
	t.Helper()
	if err := svc.ingestSvc.SaveSystemStats(ctx, ingestsvc.SaveSystemStatsRequest{
		CollectorName: collectorName,
		Stats:         stats,
	}); err != nil {
		t.Fatalf("SaveSystemStats: %v", err)
	}
}

// getLatestStats is a helper to call GetLatestStats via the ingest service.
func getLatestStats(t *testing.T, ctx context.Context, svc *Service, collectorName string) ingestsvc.SystemStat {
	t.Helper()
	stat, err := svc.ingestSvc.GetLatestStats(ctx, collectorName)
	if err != nil {
		t.Fatalf("GetLatestStats: %v", err)
	}
	return stat
}

func TestSaveSystemStats_NoData_ReturnsErrNoRows(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	_, err := svc.ingestSvc.GetLatestStats(ctx, collectorName)
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows before any save, got %v", err)
	}
}

func TestSaveSystemStats_FirstSave_CreatesOpenPeriods(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	stats := types.Stats{
		CPUUsagePercent: 42.5,
		MemTotalMB:      16384,
		MemUsedMB:       4096,
		MemAvailableMB:  12288,
		DiskTotalGB:     500,
		DiskUsedGB:      150,
		DiskAvailGB:     350,
		Timestamp:       now,
	}
	saveStats(t, ctx, svc, collectorName, stats)

	got := getLatestStats(t, ctx, svc, collectorName)

	if got.CPUUsagePercent != 42.5 {
		t.Fatalf("CPU: expected 42.5, got %f", got.CPUUsagePercent)
	}
	if got.MemTotalMb != 16384 {
		t.Fatalf("MemTotal: expected 16384, got %d", got.MemTotalMb)
	}
	if got.MemUsedMb != 4096 {
		t.Fatalf("MemUsed: expected 4096, got %d", got.MemUsedMb)
	}
	if got.DiskTotalGb != 500 {
		t.Fatalf("DiskTotal: expected 500, got %d", got.DiskTotalGb)
	}
	if got.DiskUsedGb != 150 {
		t.Fatalf("DiskUsed: expected 150, got %d", got.DiskUsedGb)
	}
	// Timestamp should be the saved timestamp
	if got.Timestamp != now.Unix() {
		t.Fatalf("Timestamp: expected %d, got %d", now.Unix(), got.Timestamp)
	}
}

func TestSaveSystemStats_NoopOnSameValue_PeriodCountUnchanged(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	stats := types.Stats{
		CPUUsagePercent: 50.0,
		MemTotalMB:      8192,
		MemUsedMB:       2048,
		MemAvailableMB:  6144,
		DiskTotalGB:     250,
		DiskUsedGB:      100,
		DiskAvailGB:     150,
		Timestamp:       now,
	}
	saveStats(t, ctx, svc, collectorName, stats)

	// Save the same values again (one second later)
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 50.0,
		MemTotalMB:      8192,
		MemUsedMB:       2048,
		MemAvailableMB:  6144,
		DiskTotalGB:     250,
		DiskUsedGB:      100,
		DiskAvailGB:     150,
		Timestamp:       now.Add(time.Second),
	})

	// The history should contain exactly 1 period per resource (no duplicate)
	history, err := svc.ingestSvc.GetStatsHistory(ctx, collectorName, now.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetStatsHistory: %v", err)
	}
	if len(history.CPUPeriods) != 1 {
		t.Fatalf("expected 1 CPU period, got %d", len(history.CPUPeriods))
	}
	if len(history.MemPeriods) != 1 {
		t.Fatalf("expected 1 Mem period, got %d", len(history.MemPeriods))
	}
	if len(history.DiskPeriods) != 1 {
		t.Fatalf("expected 1 Disk period, got %d", len(history.DiskPeriods))
	}

	// Period count is unchanged, but the latest-sample timestamp advances.
	got := getLatestStats(t, ctx, svc, collectorName)
	if got.Timestamp != now.Add(time.Second).Unix() {
		t.Fatalf("Timestamp should advance on unchanged samples: expected %d, got %d", now.Add(time.Second).Unix(), got.Timestamp)
	}
}

func TestSaveSystemStats_ValueChange_ClosesOldAndCreatesNewPeriod(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	t0 := time.Now().Truncate(time.Second).Add(-10 * time.Second)
	t1 := t0.Add(5 * time.Second)

	// First save with CPU=10
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 10.0,
		MemTotalMB:      8192,
		MemUsedMB:       2048,
		MemAvailableMB:  6144,
		DiskTotalGB:     250,
		DiskUsedGB:      100,
		DiskAvailGB:     150,
		Timestamp:       t0,
	})

	// Second save with CPU=90 (changed)
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 90.0,
		MemTotalMB:      8192,
		MemUsedMB:       2048,
		MemAvailableMB:  6144,
		DiskTotalGB:     250,
		DiskUsedGB:      100,
		DiskAvailGB:     150,
		Timestamp:       t1,
	})

	// Latest should reflect the new CPU value
	got := getLatestStats(t, ctx, svc, collectorName)
	if got.CPUUsagePercent != 90.0 {
		t.Fatalf("CPU: expected 90.0, got %f", got.CPUUsagePercent)
	}
	if got.Timestamp != t1.Unix() {
		t.Fatalf("Timestamp: expected %d (t1), got %d", t1.Unix(), got.Timestamp)
	}

	// History should contain two CPU periods
	history, err := svc.ingestSvc.GetStatsHistory(ctx, collectorName, t0.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetStatsHistory: %v", err)
	}
	if len(history.CPUPeriods) != 2 {
		t.Fatalf("expected 2 CPU periods, got %d", len(history.CPUPeriods))
	}

	// First period should be CPU=10 and closed at t1
	p0 := history.CPUPeriods[0]
	if p0.UsagePercent != 10.0 {
		t.Fatalf("period 0 CPU: expected 10.0, got %f", p0.UsagePercent)
	}
	if !p0.EndedAt.Valid {
		t.Fatal("period 0 should be closed")
	}
	if p0.EndedAt.Int64 != t1.Unix() {
		t.Fatalf("period 0 ended_at: expected %d, got %d", t1.Unix(), p0.EndedAt.Int64)
	}

	// Second period should be CPU=90 and open (no End)
	p1 := history.CPUPeriods[1]
	if p1.UsagePercent != 90.0 {
		t.Fatalf("period 1 CPU: expected 90.0, got %f", p1.UsagePercent)
	}
	if p1.EndedAt.Valid {
		t.Fatal("period 1 (open) should not have ended_at set")
	}

	// Memory should still have 1 period (unchanged)
	if len(history.MemPeriods) != 1 {
		t.Fatalf("expected 1 Mem period, got %d", len(history.MemPeriods))
	}
}

func TestSaveSystemStats_PartialCPUOnly_OtherResourcesZero(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	// Save only CPU data (all other fields zero)
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 75.0,
		MemTotalMB:      0,
		MemUsedMB:       0,
		MemAvailableMB:  0,
		DiskTotalGB:     0,
		DiskUsedGB:      0,
		DiskAvailGB:     0,
		Timestamp:       now,
	})

	got := getLatestStats(t, ctx, svc, collectorName)

	if got.CPUUsagePercent != 75.0 {
		t.Fatalf("CPU: expected 75.0, got %f", got.CPUUsagePercent)
	}
	if got.MemTotalMb != 0 {
		t.Fatalf("MemTotal: expected 0, got %d", got.MemTotalMb)
	}
	if got.DiskTotalGb != 0 {
		t.Fatalf("DiskTotal: expected 0, got %d", got.DiskTotalGb)
	}
}

func TestGetStatsHistory_SinceFilter_ExcludesOldPeriods(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)

	// Old period: starts at -3h, gets closed at -2h when the new save happens
	old := now.Add(-3 * time.Hour)
	// Recent period: starts at -2h (replaces old), still open
	recent := now.Add(-2 * time.Hour)
	// Since: -1h → the old period (ended at -2h) should be excluded,
	// but the recent period (open) should be included
	since := now.Add(-1 * time.Hour)

	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 10.0,
		MemTotalMB:      4096,
		MemUsedMB:       1024,
		MemAvailableMB:  3072,
		DiskTotalGB:     100,
		DiskUsedGB:      50,
		DiskAvailGB:     50,
		Timestamp:       old,
	})

	// This save closes the old period at -2h and creates a new open period
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 80.0,
		MemTotalMB:      8192,
		MemUsedMB:       4096,
		MemAvailableMB:  4096,
		DiskTotalGB:     200,
		DiskUsedGB:      100,
		DiskAvailGB:     100,
		Timestamp:       recent,
	})

	history, err := svc.ingestSvc.GetStatsHistory(ctx, collectorName, since)
	if err != nil {
		t.Fatalf("GetStatsHistory: %v", err)
	}

	// Only the open period (CPU=80) should be included.
	// The old period (CPU=10) ended at -2h which is before since (-1h), so it's excluded.
	if len(history.CPUPeriods) != 1 {
		t.Fatalf("expected 1 CPU period, got %d; old period should be excluded by since filter", len(history.CPUPeriods))
	}
	if history.CPUPeriods[0].UsagePercent != 80.0 {
		t.Fatalf("expected CPU 80.0, got %f", history.CPUPeriods[0].UsagePercent)
	}
	if len(history.MemPeriods) != 1 {
		t.Fatalf("expected 1 Mem period, got %d", len(history.MemPeriods))
	}
	if len(history.DiskPeriods) != 1 {
		t.Fatalf("expected 1 Disk period, got %d", len(history.DiskPeriods))
	}
}

func TestGetCollector_NoStats_ReturnsNilStats(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if c.Stats != nil {
		t.Fatal("expected nil Stats when no data")
	}
}

func TestGetCollector_WithStats_ReturnsFormattedStats(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 55.5,
		MemTotalMB:      32768,
		MemUsedMB:       16384,
		MemAvailableMB:  16384,
		DiskTotalGB:     1000,
		DiskUsedGB:      400,
		DiskAvailGB:     600,
		Timestamp:       now,
	})

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if c.Stats == nil {
		t.Fatal("expected non-nil Stats")
	}
	if c.Stats.CPUUsagePercent != 55.5 {
		t.Fatalf("CPU: expected 55.5, got %f", c.Stats.CPUUsagePercent)
	}
	if c.Stats.MemTotalMB != 32768 {
		t.Fatalf("MemTotal: expected 32768, got %d", c.Stats.MemTotalMB)
	}
	if c.Stats.DiskUsedGB != 400 {
		t.Fatalf("DiskUsed: expected 400, got %d", c.Stats.DiskUsedGB)
	}
}

func TestDeleteOldStats_RemovesOnlyClosedPeriods(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)

	// old starts at -48h, gets closed at -47h by the next save
	old := now.Add(-48 * time.Hour)
	// recent starts at -47h, still open
	recent := now.Add(-47 * time.Hour)
	// cutoff at -24h: anything closed before -24h gets deleted
	cutoff := now.Add(-24 * time.Hour)

	// Save old value (will be closed by the next save)
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 10.0,
		MemTotalMB:      4096,
		MemUsedMB:       1024,
		MemAvailableMB:  3072,
		DiskTotalGB:     100,
		DiskUsedGB:      50,
		DiskAvailGB:     50,
		Timestamp:       old,
	})

	// Save new value (closes the old period at -47h)
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 90.0,
		MemTotalMB:      8192,
		MemUsedMB:       4096,
		MemAvailableMB:  4096,
		DiskTotalGB:     200,
		DiskUsedGB:      100,
		DiskAvailGB:     100,
		Timestamp:       recent,
	})

	// Run retention: delete periods closed before cutoff (-24h)
	// The old period ended at -47h < -24h, so it should be removed.
	if err := svc.ingestSvc.DeleteOldStats(ctx, cutoff); err != nil {
		t.Fatalf("DeleteOldStats: %v", err)
	}

	// History from the beginning should only contain the open period
	// (the old closed period should be deleted because its ended_at < cutoff)
	history, err := svc.ingestSvc.GetStatsHistory(ctx, collectorName, old.Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("GetStatsHistory: %v", err)
	}

	if len(history.CPUPeriods) != 1 {
		t.Fatalf("expected 1 CPU period after retention, got %d", len(history.CPUPeriods))
	}
	if history.CPUPeriods[0].UsagePercent != 90.0 {
		t.Fatalf("expected CPU 90.0, got %f", history.CPUPeriods[0].UsagePercent)
	}
	// The remaining period must be open (open periods are never deleted)
	if history.CPUPeriods[0].EndedAt.Valid {
		t.Fatal("the remaining CPU period should be open (ended_at IS NULL)")
	}
}

func TestGetStatsHistory_PanelService_Integration(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	now := time.Now().Truncate(time.Second)

	// Save via ingest service
	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 33.3,
		MemTotalMB:      4096,
		MemUsedMB:       2048,
		MemAvailableMB:  2048,
		DiskTotalGB:     500,
		DiskUsedGB:      250,
		DiskAvailGB:     250,
		Timestamp:       now,
	})

	// Retrieve via panel service's public API
	result, err := svc.GetStatsHistory(ctx, GetStatsHistoryRequest{
		UserID:        userID,
		CollectorName: collectorName,
		Since:         now.Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetStatsHistory (panel): %v", err)
	}

	if len(result.CPUPeriods) != 1 {
		t.Fatalf("expected 1 CPU period, got %d", len(result.CPUPeriods))
	}
	if result.CPUPeriods[0].UsagePercent != 33.3 {
		t.Fatalf("CPU: expected 33.3, got %f", result.CPUPeriods[0].UsagePercent)
	}
	if len(result.MemPeriods) != 1 {
		t.Fatalf("expected 1 Mem period, got %d", len(result.MemPeriods))
	}
	if len(result.DiskPeriods) != 1 {
		t.Fatalf("expected 1 Disk period, got %d", len(result.DiskPeriods))
	}
}

func TestGetPanelStats_Success(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := t.Context()

	// Ingest a log so the app is not stray
	if _, err := svc.ingestSvc.StoreLog(ctx, ingestsvc.StoreLogRequest{
		CollectorName: collectorName,
		AppName:       appName,
		Entry: types.LogEntry{
			Timestamp: time.Now().Add(-30 * time.Minute),
			Level:     types.LevelInfo,
			Message:   "test log",
		},
	}); err != nil {
		t.Fatalf("StoreLog: %v", err)
	}

	result, err := svc.GetPanelStats(ctx, PanelStatsRequest{
		UserID: userID,
		Since:  time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetPanelStats: %v", err)
	}
	if len(result.Collectors) != 1 {
		t.Fatalf("expected 1 collector, got %d", len(result.Collectors))
	}
	cs := result.Collectors[0]
	if cs.Name != collectorName {
		t.Fatalf("expected collector %q, got %q", collectorName, cs.Name)
	}
	if len(cs.Apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(cs.Apps))
	}
	pa := cs.Apps[0]
	if pa.Name != appName {
		t.Fatalf("expected app %q, got %q", appName, pa.Name)
	}
	if pa.LogCount != 1 {
		t.Fatalf("expected log count 1, got %d", pa.LogCount)
	}
	if pa.IsStray {
		t.Fatalf("expected app not to be stray")
	}
}

func TestGetPanelStats_NoAccess(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := t.Context()

	result, err := svc.GetPanelStats(ctx, PanelStatsRequest{
		UserID: noAccessUserID,
		Since:  time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetPanelStats: %v", err)
	}
	if len(result.Collectors) != 0 {
		t.Fatalf("expected 0 collectors for no-access user, got %d", len(result.Collectors))
	}
}

func TestGetPanelStats_StrayApp(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := t.Context()

	result, err := svc.GetPanelStats(ctx, PanelStatsRequest{
		UserID: userID,
		Since:  time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetPanelStats: %v", err)
	}
	if len(result.Collectors) != 1 {
		t.Fatalf("expected 1 collector, got %d", len(result.Collectors))
	}
	cs := result.Collectors[0]
	if cs.Name != collectorName {
		t.Fatalf("expected collector %q, got %q", collectorName, cs.Name)
	}
	if len(cs.Apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(cs.Apps))
	}
	if !cs.Apps[0].IsStray {
		t.Fatalf("expected app to be stray")
	}
}

func TestComputePercentiles_Empty(t *testing.T) {
	result := PanelPercentiles{}
	if result.P50 != 0 {
		t.Fatalf("expected zero percentiles, got %+v", result)
	}
}

func TestCollectorConfigStorage(t *testing.T) {
	svc, _, _, collector, _ := setupTestService(t)
	ctx := t.Context()

	// No config stored yet: empty, no error.
	got, err := svc.FetchCollectorConfig(ctx, collector)
	if err != nil {
		t.Fatalf("get empty config: %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty config, got %q", got)
	}

	cfgJSON := `{"parsers":{"p1":{"type":"json","params":{"level_field":"level","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05.999999999Z07:00"}}},"apps":[{"name":"nginx","parser_id":"p1","source":{"type":"journalctl","params":{}}}]}`
	if err := svc.SaveCollectorConfig(ctx, collector, cfgJSON); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got, err = svc.FetchCollectorConfig(ctx, collector)
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	statsDefaulted := `{"parsers":{"p1":{"type":"json","params":{"level_field":"level","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05.999999999Z07:00"}}},"apps":[{"name":"nginx","parser_id":"p1","source":{"type":"journalctl","params":{}}}],"stats":{"interval":"30s","cpu":true,"mem":true,"disk":true}}`
	if got != statsDefaulted {
		t.Fatalf("config mismatch:\n got: %q\nwant: %q", got, statsDefaulted)
	}

	// Upsert overwrites.
	updatedJSON := `{"parsers":{"p2":{"type":"json","params":{"level_field":"level","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05.999999999Z07:00"}}},"apps":[{"name":"redis","parser_id":"p2","source":{"type":"journalctl","params":{}}}]}`
	if err := svc.SaveCollectorConfig(ctx, collector, updatedJSON); err != nil {
		t.Fatalf("update config: %v", err)
	}
	got, err = svc.FetchCollectorConfig(ctx, collector)
	if err != nil {
		t.Fatalf("get updated config: %v", err)
	}
	updatedDefaulted := `{"parsers":{"p2":{"type":"json","params":{"level_field":"level","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05.999999999Z07:00"}}},"apps":[{"name":"redis","parser_id":"p2","source":{"type":"journalctl","params":{}}}],"stats":{"interval":"30s","cpu":true,"mem":true,"disk":true}}`
	if got != updatedDefaulted {
		t.Fatalf("updated config mismatch:\n got: %q\nwant: %q", got, updatedDefaulted)
	}
}

func TestSaveCollectorConfigCreatesAppDB(t *testing.T) {
	svc, _, _, collector, _ := setupTestService(t)
	cfgJSON := `{"parsers":{"p1":{"type":"json","params":{"level_field":"level","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05.999999999Z07:00"}}},"apps":[{"name":"new-from-config","parser_id":"p1","source":{"type":"journalctl","params":{}}}]}`
	if err := svc.SaveCollectorConfig(t.Context(), collector, cfgJSON); err != nil {
		t.Fatalf("save config: %v", err)
	}

	apps, err := svc.ingestSvc.ListApps(collector)
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	found := false
	for _, name := range apps {
		if name == "new-from-config" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListApps = %v, want new-from-config", apps)
	}
}

func TestSaveCollectorConfigRequiresJSON(t *testing.T) {
	svc, _, _, collector, _ := setupTestService(t)
	if err := svc.SaveCollectorConfig(t.Context(), collector, "   "); err == nil {
		t.Fatal("expected error saving empty config")
	}
}

type fakeConfigPusher struct {
	mu         sync.Mutex
	pushedTo   string
	pushedJSON string
}

func (f *fakeConfigPusher) PushCollectorConfig(collector, configJSON string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushedTo = collector
	f.pushedJSON = configJSON
}

func TestSaveCollectorConfigPushesToConnectedCollectors(t *testing.T) {
	ctx := t.Context()
	mgr := testutil.NewTestDBManager(t)
	tracker := ingestsvc.NewTracker()
	userSvc := usersvc.NewService(testutil.NewTestDB(t))
	ingestSvc := ingestsvc.New(mgr, nil, nil, &testIngestConfig{
		sampleRate:      1.0,
		cpuDeltaPercent: 1.0,
		memDeltaKB:      64,
		diskDeltaKB:     1024,
	}, nil)
	if err := ingestSvc.RegisterCollector(ctx, ingestsvc.RegisterCollectorRequest{
		CollectorName: "push-collector",
		Apps:          []string{"a"},
	}); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	pusher := &fakeConfigPusher{}
	svc := New(mgr, ingestSvc, tracker, userSvc, pusher, nil)

	cfgJSON := `{"parsers":{"p":{"type":"json","params":{"level_field":"level","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05.999999999Z07:00"}}},"apps":[{"name":"a","parser_id":"p","source":{"type":"journalctl","params":{}}}]}`
	if err := svc.SaveCollectorConfig(ctx, "push-collector", cfgJSON); err != nil {
		t.Fatalf("save config: %v", err)
	}

	pusher.mu.Lock()
	defer pusher.mu.Unlock()
	if pusher.pushedTo != "push-collector" {
		t.Fatalf("push target = %q, want push-collector", pusher.pushedTo)
	}
	want := `{"parsers":{"p":{"type":"json","params":{"level_field":"level","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05.999999999Z07:00"}}},"apps":[{"name":"a","parser_id":"p","source":{"type":"journalctl","params":{}}}],"stats":{"interval":"30s","cpu":true,"mem":true,"disk":true}}`
	if pusher.pushedJSON != want {
		t.Fatalf("pushed JSON mismatch:\n got: %q\nwant: %q", pusher.pushedJSON, want)
	}
}

func TestDeleteCollectorApp(t *testing.T) {
	ctx := t.Context()
	mgr := testutil.NewTestDBManager(t)
	tracker := ingestsvc.NewTracker()
	userSvc := usersvc.NewService(testutil.NewTestDB(t))
	ingestSvc := ingestsvc.New(mgr, nil, nil, &testIngestConfig{
		sampleRate:      1.0,
		cpuDeltaPercent: 1.0,
		memDeltaKB:      64,
		diskDeltaKB:     1024,
	}, nil)
	if err := ingestSvc.RegisterCollector(ctx, ingestsvc.RegisterCollectorRequest{
		CollectorName: "del-collector",
		Apps:          []string{"keep", "remove"},
	}); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	pusher := &fakeConfigPusher{}
	svc := New(mgr, ingestSvc, tracker, userSvc, pusher, nil)

	cfgJSON := `{"parsers":{"p-keep":{"type":"json","params":{"level_field":"level","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05.999999999Z07:00"}},"p-remove":{"type":"json","params":{"level_field":"level","message_field":"msg","timestamp_field":"time","timestamp_format":"2006-01-02T15:04:05.999999999Z07:00"}}},"apps":[{"name":"keep","parser_id":"p-keep","source":{"type":"journalctl","params":{}}},{"name":"remove","parser_id":"p-remove","source":{"type":"journalctl","params":{}}}]}`
	if err := svc.SaveCollectorConfig(ctx, "del-collector", cfgJSON); err != nil {
		t.Fatalf("save config: %v", err)
	}

	if err := svc.DeleteCollectorApp(ctx, "del-collector", "remove"); err != nil {
		t.Fatalf("delete app: %v", err)
	}

	stored, err := svc.FetchCollectorConfig(ctx, "del-collector")
	if err != nil {
		t.Fatalf("fetch config: %v", err)
	}
	var cfg types.CollectorConfig
	if err := json.Unmarshal([]byte(stored), &cfg); err != nil {
		t.Fatalf("unmarshal stored config: %v", err)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Name != "keep" {
		t.Fatalf("config apps = %v, want only keep", cfg.Apps)
	}
	if _, ok := cfg.Parsers["p-keep"]; !ok {
		t.Fatalf("parser p-keep must survive removal of the other app, parsers = %v", cfg.Parsers)
	}
	if _, ok := cfg.Parsers["p-remove"]; ok {
		t.Fatalf("parser p-remove should be dropped, parsers = %v", cfg.Parsers)
	}

	apps, err := mgr.ListApps("del-collector")
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	if len(apps) != 1 || apps[0] != "keep" {
		t.Fatalf("listed apps = %v, want only keep", apps)
	}

	pusher.mu.Lock()
	pushed := pusher.pushedJSON
	pusher.mu.Unlock()
	if strings.Contains(pushed, "remove") {
		t.Fatalf("pusher received config still containing removed app: %s", pushed)
	}
}

func TestBuildCollectorConfig(t *testing.T) {
	src := types.SourceSpec{Type: "journalctl", Params: map[string]string{"unit": "nginx.service"}}
	parser := types.ParserSpec{Type: "json", Params: map[string]string{"level_field": "level"}}

	var cfg types.CollectorConfig
	if err := cfg.AddApp("nginx", src, parser); err != nil {
		t.Fatalf("AddApp: %v", err)
	}
	if len(cfg.Apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(cfg.Apps))
	}

	if err := cfg.AddApp("redis", src, parser); err != nil {
		t.Fatalf("AddApp second: %v", err)
	}
	if len(cfg.Apps) != 2 {
		t.Fatalf("expected 2 apps, got %d", len(cfg.Apps))
	}
}

func TestBuildUptimeAppConfig(t *testing.T) {
	var cfg types.CollectorConfig
	if err := cfg.AddUptimeApp("status-page", "https://example.com", "30s", "200"); err != nil {
		t.Fatalf("AddUptimeApp: %v", err)
	}
	if len(cfg.Apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(cfg.Apps))
	}
}

func TestRemoveApp(t *testing.T) {
	src := types.SourceSpec{Type: "journalctl", Params: map[string]string{"unit": "nginx.service"}}
	parser := types.ParserSpec{Type: "json", Params: map[string]string{"level_field": "level"}}

	var cfg types.CollectorConfig
	if err := cfg.AddApp("nginx", src, parser); err != nil {
		t.Fatalf("AddApp: %v", err)
	}
	if err := cfg.AddApp("redis", src, parser); err != nil {
		t.Fatalf("AddApp second: %v", err)
	}

	cfg.RemoveApp("nginx")
	if len(cfg.Apps) != 1 {
		t.Fatalf("expected 1 app after remove, got %d", len(cfg.Apps))
	}

	// Re-add nginx should work
	if err := cfg.AddApp("nginx", src, parser); err != nil {
		t.Fatalf("re-add nginx: %v", err)
	}
	if len(cfg.Apps) != 2 {
		t.Fatalf("expected 2 apps after re-add, got %d", len(cfg.Apps))
	}
}

type fakeFormatGuesser struct {
	result types.FormatResultEvent
	err    error
}

func (f *fakeFormatGuesser) GuessFormat(ctx context.Context, collector string, req types.FormatRequestEvent) (types.FormatResultEvent, error) {
	return f.result, f.err
}

type recordingFormatGuesser struct {
	services  []string
	byService map[string]types.FormatResultEvent
	err       error
}

func (f *recordingFormatGuesser) GuessFormat(ctx context.Context, collector string, req types.FormatRequestEvent) (types.FormatResultEvent, error) {
	f.services = append(f.services, req.Service)
	if f.err != nil {
		return types.FormatResultEvent{}, f.err
	}
	if f.byService != nil {
		if result, ok := f.byService[req.Service]; ok {
			return result, nil
		}
	}
	return types.FormatResultEvent{NotFound: true}, nil
}

func slogtextGuess(unit string) types.FormatResultEvent {
	return types.FormatResultEvent{
		App: types.AppSpec{
			Source: &types.SourceSpec{Type: "journalctl", Params: map[string]string{"unit": unit + ".service"}},
		},
		Parser: types.ParserSpec{
			Type: "slogtext",
			Params: map[string]string{
				"level_field":      "level",
				"message_field":    "msg",
				"timestamp_field":  "time",
				"timestamp_format": "2006-01-02T15:04:05.999999999Z07:00",
			},
		},
	}
}

func TestPreviewFormat(t *testing.T) {
	ctx := t.Context()
	guesser := &fakeFormatGuesser{
		result: types.FormatResultEvent{
			App: types.AppSpec{
				Source: &types.SourceSpec{Type: "journalctl", Params: map[string]string{"unit": "nginx.service"}},
			},
			Parser:  types.ParserSpec{Type: "json", Params: map[string]string{"level_field": "level"}},
			Samples: []string{`{"level":"info"}`},
		},
	}
	svc := New(nil, nil, nil, nil, nil, guesser)

	result, err := svc.PreviewFormat(ctx, "collector-a", "nginx", types.ParserSpec{})
	if err != nil {
		t.Fatalf("PreviewFormat: %v", err)
	}
	if result.App.Source.Type != "journalctl" {
		t.Fatalf("source type = %q, want journalctl", result.App.Source.Type)
	}
	if result.Parser.Type != "json" {
		t.Fatalf("parser type = %q, want json", result.Parser.Type)
	}
	if len(result.Samples) != 1 {
		t.Fatalf("samples = %v", result.Samples)
	}
}

func TestPreviewFormatNilGuesser(t *testing.T) {
	svc := New(nil, nil, nil, nil, nil, nil)
	if _, err := svc.PreviewFormat(t.Context(), "collector-a", "nginx", types.ParserSpec{}); err == nil {
		t.Fatal("expected error when guesser is unavailable")
	}
}

func TestGuessFormatAndSaveNoSource(t *testing.T) {
	ctx := t.Context()
	svc := New(nil, nil, nil, nil, nil, &fakeFormatGuesser{
		result: types.FormatResultEvent{},
	})

	err := svc.GuessFormatAndSave(ctx, "collector-a", "nginx")
	if !errors.Is(err, ErrAppNoSource) {
		t.Fatalf("GuessFormatAndSave error = %v, want ErrAppNoSource", err)
	}
}

func TestGuessFormatAndSaveNotFound(t *testing.T) {
	ctx := t.Context()
	svc := New(nil, nil, nil, nil, nil, &fakeFormatGuesser{
		result: types.FormatResultEvent{
			Error:    "no logs found in the source",
			NotFound: true,
		},
	})

	err := svc.GuessFormatAndSave(ctx, "collector-a", "nginx")
	if !errors.Is(err, ErrAppNoSource) {
		t.Fatalf("GuessFormatAndSave error = %v, want ErrAppNoSource", err)
	}
}
