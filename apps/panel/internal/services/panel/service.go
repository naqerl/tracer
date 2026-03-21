package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/user"
	sharedtypes "tracer/libs/types"
)

var (
	ErrCollectorNotFound = errors.New("collector not found")
	ErrAppNotFound       = errors.New("app not found")
	ErrLogNotFound       = errors.New("log not found")
	ErrAccessDenied      = errors.New("access denied")
	ErrInvalidCollector  = errors.New("invalid collector name")
	ErrInvalidApp        = errors.New("invalid app name")
	ErrInvalidGroupID    = errors.New("invalid group ID")
	ErrInvalidLogID      = errors.New("invalid log ID")
	ErrInvalidOffset     = errors.New("invalid offset")
	ErrInvalidLimit      = errors.New("invalid limit")
)

const (
	maxSinceAge         = 30 * 24 * time.Hour
	sinceValidationSkew = time.Second
)

func New(dbManager *dbmanager.Manager, ingestSvc *ingest.Service, tracker *ingest.Tracker, userSvc *user.Service) *Service {
	return &Service{
		dbManager: dbManager,
		ingestSvc: ingestSvc,
		userSvc:   userSvc,
		tracker:   tracker,
	}
}

type Service struct {
	dbManager *dbmanager.Manager
	ingestSvc *ingest.Service
	userSvc   *user.Service
	tracker   *ingest.Tracker
}

// Collector provides a complete view of a collector for display,
// with data pre-filtered according to user permissions.
type Collector struct {
	Name        string
	RemoteAddr  string
	IsConnected bool
	ConnectedAt time.Time
	LastSeenAt  time.Time
	Stats       *CollectorStats
}

// CollectorStats holds the collector stats required by the UI.
type CollectorStats struct {
	CPUUsagePercent    float64
	MemTotalMB         int64
	MemUsedMB          int64
	DiskTotalGB        int64
	DiskUsedGB         int64
	TimestampFormatted string
	Staleness          string // "fresh", "warning", "stale", "old"
}

// ListCollectors returns collectors the user can access.
// Tracker data only enriches connection metadata.
func (s *Service) ListCollectors(ctx context.Context, userID int64) ([]Collector, error) {
	collectors, err := s.listAllCollectors(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not list all collectors: %w", err)
	}

	access, err := s.userSvc.FindAccessPolicy(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("could not load apps access for user %d: %w", userID, err)
	}

	var i int
	for _, collector := range collectors {
		if !access.AllowedTo(collector.Name, "") {
			continue
		}
		collectors[i] = collector
		i++
	}

	return collectors[:i], nil
}

type GetCollectorRequest struct {
	UserID        int64
	CollectorName string
}

func (r GetCollectorRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	return nil
}

func (s *Service) GetCollector(ctx context.Context, req GetCollectorRequest) (Collector, error) {
	exists, err := s.collectorExists(req.CollectorName)
	if err != nil {
		return Collector{}, fmt.Errorf("could not ensure collector existence: %w", err)
	}
	if !exists {
		return Collector{}, ErrCollectorNotFound
	}

	collector, err := s.getCollector(ctx, req.CollectorName)
	if err != nil {
		return Collector{}, fmt.Errorf("could not load collector data for %s: %w", req.CollectorName, err)
	}

	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return Collector{}, fmt.Errorf("could not load access for user %d: %w", req.UserID, err)
	}
	if !access.AllowedTo(req.CollectorName, "") {
		return Collector{}, ErrAccessDenied
	}

	return collector, nil
}

func (s *Service) collectorExists(name string) (bool, error) {
	collectors, err := s.dbManager.ListCollectors()
	if err != nil {
		return false, err
	}
	for _, collector := range collectors {
		if collector == name {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) listAllCollectors(ctx context.Context) ([]Collector, error) {
	collectorNames, err := s.dbManager.ListCollectors()
	if err != nil {
		return nil, fmt.Errorf("list collectors: %w", err)
	}

	views := make([]Collector, 0, len(collectorNames))
	for _, collectorName := range collectorNames {
		view, err := s.getCollector(ctx, collectorName)
		if err != nil {
			slog.Error("Failed to get collector", "collector", collectorName, "error", err)
			continue
		}

		views = append(views, view)
	}

	return views, nil
}

func (s *Service) getCollector(ctx context.Context, collectorName string) (Collector, error) {
	view := Collector{
		Name: collectorName,
	}

	if trackerInfo, exists := s.tracker.Get(view.Name); exists {
		view.RemoteAddr = trackerInfo.RemoteAddr
		view.IsConnected = trackerInfo.IsConnected
		view.ConnectedAt = trackerInfo.ConnectedAt
		view.LastSeenAt = trackerInfo.LastSeenAt
	}

	stats, err := s.ingestSvc.GetLatestStats(ctx, collectorName)
	if err == nil {
		view.Stats = formatCollectorStats(stats)
		return view, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return view, nil
	}

	return view, err
}

type ListCollectorAppsRequest struct {
	UserID        int64
	CollectorName string
}

func (r ListCollectorAppsRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	return nil
}

func (s *Service) ListCollectorApps(ctx context.Context, req ListCollectorAppsRequest) ([]string, error) {
	exists, err := s.collectorExists(req.CollectorName)
	if err != nil {
		return nil, fmt.Errorf("could not ensure collector existence: %w", err)
	}
	if !exists {
		return nil, ErrCollectorNotFound
	}

	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("could not load access for user %d: %w", req.UserID, err)
	}
	if !access.AllowedTo(req.CollectorName, "") {
		return nil, ErrAccessDenied
	}

	apps, err := s.ingestSvc.ListApps(req.CollectorName)
	if err != nil {
		return nil, fmt.Errorf("could not list apps for collector %s: %w", req.CollectorName, err)
	}
	if access.Apps == nil {
		return apps, nil
	}

	filtered := make([]string, 0, len(apps))
	for _, app := range apps {
		if access.AllowedTo(req.CollectorName, app) {
			filtered = append(filtered, app)
		}
	}
	return filtered, nil
}

type AppHealth struct {
	CurrentStatus sharedtypes.HealthStatus
	PeriodCount   int
	UptimePercent float64
	Buckets       []HealthBucket
}

type HealthBucket struct {
	Start        time.Time
	End          time.Time
	UpDuration   time.Duration
	DownDuration time.Duration
}

type GetAppHealthRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	Since         time.Time
}

func (r GetAppHealthRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	return nil
}

func (s *Service) GetAppHealth(ctx context.Context, req GetAppHealthRequest) (AppHealth, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return AppHealth{}, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return AppHealth{}, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return AppHealth{}, err
	}

	return s.readAppHealth(ctx, queries, req.Since, true)
}

type GetAppErrorCountRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	Since         time.Time
}

func (r GetAppErrorCountRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	return nil
}

func (s *Service) GetAppErrorCount(ctx context.Context, req GetAppErrorCountRequest) (int64, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return 0, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return 0, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return 0, err
	}

	counts, err := queries.GetPercentiles(ctx, req.Since.Unix())
	if err != nil {
		return 0, err
	}

	return counts.Errors, nil
}

type GetStatsHistoryRequest struct {
	UserID        int64
	CollectorName string
	Since         time.Time
}

func (r GetStatsHistoryRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	return nil
}

func sinceWithinRange(since time.Time) bool {
	return time.Since(since) <= maxSinceAge+sinceValidationSkew
}

func (s *Service) GetStatsHistory(ctx context.Context, req GetStatsHistoryRequest) ([]db.SystemStat, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, "") {
		return nil, ErrAccessDenied
	}

	return s.ingestSvc.GetStatsHistory(ctx, req.CollectorName, req.Since)
}

type GetAppStatsRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	Since         time.Time
}

type AppStats struct {
	Total    int64
	Errors   int64
	Warnings int64
	Infos    int64
}

func (r GetAppStatsRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	return nil
}

func (s *Service) GetAppStats(ctx context.Context, req GetAppStatsRequest) (AppStats, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return AppStats{}, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return AppStats{}, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return AppStats{}, err
	}

	counts, err := queries.GetPercentiles(ctx, req.Since.Unix())
	if err != nil {
		return AppStats{}, err
	}

	return AppStats{
		Total:    counts.Total,
		Errors:   counts.Errors,
		Warnings: counts.Warnings,
		Infos:    counts.Infos,
	}, nil
}

type GetLogGroupsRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	Level         string
	Since         time.Time
	Offset        int
	Limit         int
}

type LogGroup struct {
	ID        int64
	Message   string
	Level     string
	Count     int64
	LastLogAt time.Time
	CreatedAt time.Time
}

func (r GetLogGroupsRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.Offset < 0 {
		return fmt.Errorf("offset must be non-negative")
	}
	if r.Limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}
	return nil
}

func (s *Service) GetLogGroups(ctx context.Context, req GetLogGroupsRequest) ([]LogGroup, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return nil, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return nil, err
	}

	level := req.Level
	if level == "all" {
		level = "%"
	}

	rows, err := queries.ListLogGroups(ctx, db.ListLogGroupsParams{
		CreatedAt: req.Since.Unix(),
		Level:     level,
		Limit:     int64(req.Limit),
		Offset:    int64(req.Offset),
	})
	if err != nil {
		return nil, err
	}

	groups := make([]LogGroup, 0, len(rows))
	for _, row := range rows {
		groups = append(groups, LogGroup{
			ID:        row.ID,
			Message:   row.Message,
			Level:     row.Level,
			Count:     row.Count,
			LastLogAt: time.Unix(row.LastLogAt, 0),
			CreatedAt: time.Unix(row.CreatedAt, 0),
		})
	}

	return groups, nil
}

type GetLogGroupRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	GroupID       int64
}

func (r GetLogGroupRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.GroupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	return nil
}

func (s *Service) GetLogGroup(ctx context.Context, req GetLogGroupRequest) (LogGroup, error) {
	var group LogGroup

	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return group, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return group, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return group, err
	}

	row, err := queries.GetLogGroupByID(ctx, sql.NullInt64{Int64: req.GroupID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return group, ErrLogNotFound
	}
	if err != nil {
		return group, err
	}

	return LogGroup{
		ID:        req.GroupID,
		Message:   row.Message,
		Level:     row.Level,
		Count:     row.LogCount,
		CreatedAt: time.Unix(row.CreatedAt, 0),
		LastLogAt: time.Unix(row.LastLogAt, 0),
	}, nil
}

type GroupLogEntry struct {
	ID        int64
	Level     string
	Context   sql.NullString
	CreatedAt time.Time
}

type ListGroupLogsRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	GroupID       int64
	Since         time.Time
	Offset        int
	Limit         int
}

func (r ListGroupLogsRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.GroupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	if r.Offset < 0 {
		return fmt.Errorf("offset must be non-negative")
	}
	if r.Limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}
	return nil
}

func (s *Service) ListGroupLogs(ctx context.Context, req ListGroupLogsRequest) ([]GroupLogEntry, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return nil, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return nil, err
	}

	rows, err := queries.GetLogsByGroupID(ctx, db.GetLogsByGroupIDParams{
		GroupID:   sql.NullInt64{Int64: req.GroupID, Valid: true},
		CreatedAt: req.Since.Unix(),
		Limit:     int64(req.Limit),
		Offset:    int64(req.Offset),
	})
	if err != nil {
		return nil, err
	}

	entries := make([]GroupLogEntry, len(rows))
	for i, row := range rows {
		entries[i] = GroupLogEntry{
			ID:        row.ID,
			Level:     row.Level,
			Context:   row.Context,
			CreatedAt: time.Unix(row.CreatedAt, 0),
		}
	}

	return entries, nil
}

type GetLogByIDRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	ID            int64
}

func (r GetLogByIDRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.ID <= 0 {
		return fmt.Errorf("log ID must be positive")
	}
	return nil
}

func (s *Service) GetLogByID(ctx context.Context, req GetLogByIDRequest) (db.Log, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return db.Log{}, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return db.Log{}, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return db.Log{}, err
	}

	logEntry, err := queries.GetLogByID(ctx, req.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.Log{}, ErrLogNotFound
		}
		return db.Log{}, fmt.Errorf("get log: %w", err)
	}

	return logEntry, nil
}

type DeleteLogGroupRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	GroupID       int64
}

func (r DeleteLogGroupRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.GroupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	return nil
}

func (s *Service) DeleteLogGroup(ctx context.Context, req DeleteLogGroupRequest) error {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return err
	}

	// Then delete the group
	rowsAffected, err := queries.DeleteLogGroup(ctx, req.GroupID)
	if err != nil {
		return fmt.Errorf("delete log group: %w", err)
	}
	if rowsAffected == 0 {
		return ErrLogNotFound
	}

	return nil
}

func (s *Service) GetLogVolumeTimeSeries(ctx context.Context, collectorName, appName string, since time.Time, granularity string) ([]db.GetLogVolumeTimeSeriesRow, error) {
	queries, err := s.queriesForApp(collectorName, appName)
	if err != nil {
		return nil, err
	}

	return queries.GetLogVolumeTimeSeries(ctx, db.GetLogVolumeTimeSeriesParams{
		Since:       since.Unix(),
		Granularity: granularity,
	})
}

func (s *Service) queriesForApp(collectorName, appName string) (*db.Queries, error) {
	sqlDB, err := s.dbManager.GetAppDB(collectorName, appName)
	if err != nil {
		if err == dbmanager.ErrDBNotFound {
			return nil, ErrAppNotFound
		}
		return nil, err
	}
	return db.New(sqlDB), nil
}

func formatCollectorStats(stats db.SystemStat) *CollectorStats {
	now := time.Now()
	createdAt := time.Unix(stats.CreatedAt, 0)
	age := now.Sub(createdAt)

	nowY, nowM, nowD := now.Date()
	statY, statM, statD := createdAt.Date()
	sameDate := nowY == statY && nowM == statM && nowD == statD

	var staleness string
	var formatted string

	if !sameDate {
		staleness = "old"
		formatted = createdAt.Format("2006-01-02 15:04:05")
	} else if age > 30*time.Minute {
		staleness = "stale"
		formatted = createdAt.Format("15:04:05")
	} else if age > 5*time.Minute {
		staleness = "warning"
		formatted = createdAt.Format("15:04:05")
	} else {
		staleness = "fresh"
		formatted = createdAt.Format("15:04:05")
	}

	return &CollectorStats{
		CPUUsagePercent:    stats.CpuUsagePercent,
		MemTotalMB:         stats.MemTotalMb,
		MemUsedMB:          stats.MemUsedMb,
		DiskTotalGB:        stats.DiskTotalGb,
		DiskUsedGB:         stats.DiskUsedGb,
		TimestampFormatted: formatted,
		Staleness:          staleness,
	}
}

func (s *Service) readAppHealth(ctx context.Context, queries *db.Queries, since time.Time, withBars bool) (AppHealth, error) {
	health := AppHealth{}

	latest, err := queries.GetLatestHealthPeriod(ctx)
	switch {
	case err == nil:
		health.CurrentStatus = sharedtypes.HealthStatus(latest.Status)
	case errors.Is(err, sql.ErrNoRows):
	default:
		return health, err
	}

	now := time.Now()
	periods, err := queries.ListHealthPeriodsOverlappingRange(ctx, db.ListHealthPeriodsOverlappingRangeParams{
		StartedAt: now.Unix(),
		EndedAt:   sql.NullInt64{Int64: since.Unix(), Valid: true},
	})
	if err != nil {
		return health, err
	}

	health.PeriodCount = len(periods)
	upDuration := time.Duration(0)
	downDuration := time.Duration(0)
	for _, period := range periods {
		overlap := overlapDuration(period, since, now)
		if overlap <= 0 {
			continue
		}
		switch sharedtypes.HealthStatus(period.Status) {
		case sharedtypes.HealthStatusUp:
			upDuration += overlap
		case sharedtypes.HealthStatusDown:
			downDuration += overlap
		}
	}

	totalDuration := upDuration + downDuration
	if totalDuration > 0 {
		health.UptimePercent = math.Round((float64(upDuration) / float64(totalDuration) * 10000)) / 100
	}

	if withBars {
		health.Buckets = buildHealthBuckets(periods, since, now)
	}

	return health, nil
}

func buildHealthBuckets(periods []db.AppHealthPeriod, since, now time.Time) []HealthBucket {
	if now.Before(since) {
		now = since
	}

	bucketCount := len(periods)
	if bucketCount < 37 {
		bucketCount = 37
	}

	window := now.Sub(since)
	if window <= 0 {
		return []HealthBucket{{Start: since, End: now}}
	}

	bucketSize := time.Duration(float64(window) / float64(bucketCount))
	if bucketSize <= 0 {
		bucketSize = time.Second
	}

	buckets := make([]HealthBucket, 0, bucketCount)
	current := since
	for i := 0; i < bucketCount; i++ {
		next := current.Add(bucketSize)
		if i == bucketCount-1 || next.After(now) {
			next = now
		}

		bucket := HealthBucket{
			Start: current,
			End:   next,
		}
		for _, period := range periods {
			overlap := overlapDuration(period, current, next)
			if overlap <= 0 {
				continue
			}
			switch sharedtypes.HealthStatus(period.Status) {
			case sharedtypes.HealthStatusUp:
				bucket.UpDuration += overlap
			case sharedtypes.HealthStatusDown:
				bucket.DownDuration += overlap
			}
		}
		buckets = append(buckets, bucket)
		current = next
	}

	return buckets
}

func overlapDuration(period db.AppHealthPeriod, start, end time.Time) time.Duration {
	periodStart := time.Unix(period.StartedAt, 0)
	periodEnd := end
	if period.EndedAt.Valid {
		periodEnd = time.Unix(period.EndedAt.Int64, 0)
	}
	if periodEnd.Before(periodStart) {
		periodEnd = periodStart
	}

	overlapStart := maxTime(start, periodStart)
	overlapEnd := minTime(end, periodEnd)
	if !overlapEnd.After(overlapStart) {
		return 0
	}
	return overlapEnd.Sub(overlapStart)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
