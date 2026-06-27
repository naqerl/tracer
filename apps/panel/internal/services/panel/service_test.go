package panel

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
	ingestsvc "tracer/apps/panel/internal/services/ingest"
	usersvc "tracer/apps/panel/internal/services/user"
	panelmigrations "tracer/apps/panel/sql/panel"
	"tracer/libs/types"

	"github.com/pressly/goose/v3"
)

// testIngestConfig implements ingest.IngestConfig for testing.
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

// newTestPanelDB creates an in-memory panel database with migrations applied.
func newTestPanelDB(t *testing.T) *dbmanager.DBHandle {
	t.Helper()

	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open panel db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, panelmigrations.Embed)
	if err != nil {
		t.Fatalf("create goose provider: %v", err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	return dbmanager.NewDBHandle(db, "test-panel")
}

// newTestDBManager creates a dbmanager.Manager backed by a temp directory.
func newTestDBManager(t *testing.T) *dbmanager.Manager {
	t.Helper()

	dir := t.TempDir()
	mgr, err := dbmanager.New(dir)
	if err != nil {
		t.Fatalf("create dbmanager: %v", err)
	}
	return mgr
}

// setupTestService creates a fully wired panel Service for testing.
// It creates a test user (admin), registers a collector+app DB, and grants access.
func setupTestService(t *testing.T) (*Service, *dbmanager.DBHandle, int64, string, string) {
	t.Helper()
	ctx := context.Background()

	// --- Panel DB ---
	panelDB := newTestPanelDB(t)

	// --- DBManager ---
	mgr := newTestDBManager(t)

	// --- Tracker ---
	tracker := ingestsvc.NewTracker()

	// --- User service ---
	userSvc := usersvc.NewService(panelDB)

	// --- Auth service (to create user) ---
	authSvc := authsvc.New(panelDB)

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
	})
	if err := ingestSvc.RegisterCollector(ctx, ingestsvc.RegisterCollectorRequest{
		CollectorName: "test-collector",
		Apps:          []string{"test-app"},
	}); err != nil {
		t.Fatalf("register collector: %v", err)
	}

	// --- Create the panel service ---
	svc := New(mgr, ingestSvc, tracker, userSvc)

	return svc, panelDB, user.ID, "test-collector", "test-app"
}

// setupTestServiceNoAccess creates a service with a user that has no access grants.
func setupTestServiceNoAccess(t *testing.T) (*Service, int64) {
	t.Helper()
	ctx := context.Background()

	panelDB := newTestPanelDB(t)
	mgr := newTestDBManager(t)
	tracker := ingestsvc.NewTracker()
	userSvc := usersvc.NewService(panelDB)
	authSvc := authsvc.New(panelDB)

	// Register collector anyway (so it exists)
	ingestSvc := ingestsvc.New(mgr, nil, nil, &testIngestConfig{
		sampleRate:      1.0,
		cpuDeltaPercent: 1.0,
		memDeltaKB:      64,
		diskDeltaKB:     1024,
	})
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

	svc := New(mgr, ingestSvc, tracker, userSvc)
	return svc, user.ID
}

// --- Tests ---

func TestListCollectors_NoCollectors(t *testing.T) {
	svc, _, userID, _, _ := setupTestService(t)

	collectors, err := svc.ListCollectors(context.Background(), userID)
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

func TestListCollectors_AccessFiltering(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)

	collectors, err := svc.ListCollectors(context.Background(), noAccessUserID)
	if err != nil {
		t.Fatalf("ListCollectors: %v", err)
	}
	if len(collectors) != 0 {
		t.Fatalf("expected 0 collectors for user without access, got %d", len(collectors))
	}
}

func TestGetCollector_Found(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := context.Background()

	// Insert some stats so the collector has data
	_ = svc // We'll use the mgr through svc's internal fields

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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

func TestGetAppHealth_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := context.Background()

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
	ctx := context.Background()

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

func TestGetAppErrorCount_Success(t *testing.T) {
	svc, _, userID, collectorName, appName := setupTestService(t)
	ctx := context.Background()

	count, err := svc.GetAppErrorCount(ctx, GetAppErrorCountRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		Since:         time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("GetAppErrorCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 errors, got %d", count)
	}
}

func TestGetAppErrorCount_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := context.Background()

	_, err := svc.GetAppErrorCount(ctx, GetAppErrorCountRequest{
		UserID:        noAccessUserID,
		CollectorName: "other-collector",
		AppName:       "other-app",
		Since:         time.Now().Add(-1 * time.Hour),
	})
	if err != ErrAccessDenied {
		t.Fatalf("expected ErrAccessDenied, got %v", err)
	}
}

func TestGetAppErrorCount_InvalidRequest(t *testing.T) {
	tests := []struct {
		name string
		req  GetAppErrorCountRequest
	}{
		{name: "zero user ID", req: GetAppErrorCountRequest{UserID: 0, CollectorName: "c", AppName: "a", Since: time.Now()}},
		{name: "empty collector", req: GetAppErrorCountRequest{UserID: 1, CollectorName: "", AppName: "a", Since: time.Now()}},
		{name: "empty app", req: GetAppErrorCountRequest{UserID: 1, CollectorName: "c", AppName: "", Since: time.Now()}},
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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
}

func TestGetAppStats_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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

func TestGetLogGroup_AccessDenied(t *testing.T) {
	svc, noAccessUserID := setupTestServiceNoAccess(t)
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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

// TestCollectorStats_Staleness_Stale verifies staleness="stale" for a 45-min-old stat.
func TestCollectorStats_Staleness_Stale(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := context.Background()

	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 10.0,
		MemTotalMB:      4096,
		MemUsedMB:       1024,
		MemAvailableMB:  3072,
		DiskTotalGB:     100,
		DiskUsedGB:      50,
		DiskAvailGB:     50,
		Timestamp:       time.Now().Add(-45 * time.Minute),
	})

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if c.Stats.Staleness != "stale" {
		t.Fatalf("expected stale, got %q", c.Stats.Staleness)
	}
}

// TestCollectorStats_Staleness_Warning verifies staleness="warning" for a 7-min-old stat.
func TestCollectorStats_Staleness_Warning(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := context.Background()

	saveStats(t, ctx, svc, collectorName, types.Stats{
		CPUUsagePercent: 10.0,
		MemTotalMB:      4096,
		MemUsedMB:       1024,
		MemAvailableMB:  3072,
		DiskTotalGB:     100,
		DiskUsedGB:      50,
		DiskAvailGB:     50,
		Timestamp:       time.Now().Add(-7 * time.Minute),
	})

	c, err := svc.GetCollector(ctx, GetCollectorRequest{
		UserID:        userID,
		CollectorName: collectorName,
	})
	if err != nil {
		t.Fatalf("GetCollector: %v", err)
	}
	if c.Stats.Staleness != "warning" {
		t.Fatalf("expected warning, got %q", c.Stats.Staleness)
	}
}

// TestCollectorStats_Staleness_Old verifies staleness="old" for a 24h-old stat.
func TestCollectorStats_Staleness_Old(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := context.Background()

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

	buckets := buildHealthBuckets(periods, since, now)
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

// --- Edge cases ---

func TestGetCollector_WithStats(t *testing.T) {
	svc, _, userID, collectorName, _ := setupTestService(t)
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

	_, err := svc.ingestSvc.GetLatestStats(ctx, collectorName)
	if err != sql.ErrNoRows {
		t.Fatalf("expected sql.ErrNoRows before any save, got %v", err)
	}
}

func TestSaveSystemStats_FirstSave_CreatesOpenPeriods(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := context.Background()

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
	ctx := context.Background()

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

	// The open period timestamp should still be the first save (no update)
	got := getLatestStats(t, ctx, svc, collectorName)
	if got.Timestamp != now.Unix() {
		t.Fatalf("Timestamp should remain from first save: expected %d, got %d", now.Unix(), got.Timestamp)
	}
}

func TestSaveSystemStats_ValueChange_ClosesOldAndCreatesNewPeriod(t *testing.T) {
	svc, _, _, collectorName, _ := setupTestService(t)
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
	ctx := context.Background()

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
