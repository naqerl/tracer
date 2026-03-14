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

type Service struct {
	dbManager *dbmanager.Manager
	ingestSvc *ingest.Service
	tracker   *ingest.Tracker
	userSvc   *user.Service
}

func New(dbManager *dbmanager.Manager, ingestSvc *ingest.Service, tracker *ingest.Tracker, userSvc *user.Service) *Service {
	return &Service{
		dbManager: dbManager,
		ingestSvc: ingestSvc,
		tracker:   tracker,
		userSvc:   userSvc,
	}
}

// StatsDisplay holds formatted stats data for UI rendering.
type StatsDisplay struct {
	TimestampFormatted string
	Staleness          string // "fresh", "warning", "stale", "old"
}

type HealthBucket struct {
	Start        time.Time
	End          time.Time
	UpDuration   time.Duration
	DownDuration time.Duration
}

type AppHealth struct {
	CurrentStatus sharedtypes.HealthStatus
	PeriodCount   int
	UptimePercent float64
	Buckets       []HealthBucket
}

type App struct {
	Name   string
	Health AppHealth
}

// CollectorView provides a complete view of a collector for display,
// with data pre-filtered according to user permissions.
type CollectorView struct {
	Name         string
	RemoteAddr   string
	IsConnected  bool
	ConnectedAt  time.Time
	LastSeenAt   time.Time
	Apps         []App
	Stats        db.SystemStat
	StatsDisplay StatsDisplay
}

// ListCollectors returns collectors without loading their apps.
// Tracker data only enriches connection metadata.
func (s *Service) ListCollectors(ctx context.Context, user sharedtypes.User) ([]CollectorView, error) {
	collectors, err := s.listCollectors(ctx)
	if err != nil {
		return nil, err
	}
	if user.IsAdmin {
		return collectors, nil
	}

	allowedApps, err := s.userAppAccessSet(ctx, user)
	if err != nil {
		return nil, err
	}

	filtered := make([]CollectorView, 0, len(collectors))
	for _, collector := range collectors {
		if len(collector.Apps) == 0 {
			if len(allowedApps[collector.Name]) > 0 {
				filtered = append(filtered, collector)
			}
			continue
		}

		collector.Apps = filterApps(collector.Apps, allowedApps[collector.Name])
		if len(collector.Apps) > 0 {
			filtered = append(filtered, collector)
		}
	}

	return filtered, nil
}

type GetCollectorDetailRequest struct {
	CollectorName string
}

func (r GetCollectorDetailRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	return nil
}

func (s *Service) GetCollector(ctx context.Context, user sharedtypes.User, req GetCollectorDetailRequest) (CollectorView, error) {
	exists, err := s.collectorExists(req.CollectorName)
	if err != nil {
		return CollectorView{}, err
	}
	if !exists {
		return CollectorView{}, ErrCollectorNotFound
	}

	apps, err := s.loadCollectorApps(ctx, req.CollectorName, time.Time{}, false, false)
	if err != nil {
		return CollectorView{}, err
	}

	view, err := s.getCollectorBase(ctx, req.CollectorName)
	if err != nil {
		return CollectorView{}, err
	}
	view.Apps = apps

	if user.IsAdmin {
		return view, nil
	}

	allowedApps, err := s.userAppAccessSet(ctx, user)
	if err != nil {
		return CollectorView{}, err
	}

	view.Apps = filterApps(view.Apps, allowedApps[view.Name])
	if len(view.Apps) == 0 {
		return CollectorView{}, ErrAccessDenied
	}

	return view, nil
}

type GetCollectorAppsRequest struct {
	CollectorName string
}

func (r GetCollectorAppsRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	return nil
}

func (s *Service) GetCollectorApps(ctx context.Context, user sharedtypes.User, req GetCollectorAppsRequest) ([]App, error) {
	exists, err := s.collectorExists(req.CollectorName)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrCollectorNotFound
	}

	apps, err := s.loadCollectorApps(ctx, req.CollectorName, time.Time{}, false, false)
	if err != nil {
		return nil, err
	}

	if user.IsAdmin {
		return apps, nil
	}

	allowedApps, err := s.userAppAccessSet(ctx, user)
	if err != nil {
		return nil, err
	}

	filtered := filterApps(apps, allowedApps[req.CollectorName])
	if len(filtered) == 0 {
		return nil, ErrAccessDenied
	}

	return filtered, nil
}

func (s *Service) listCollectors(ctx context.Context) ([]CollectorView, error) {
	collectorNames, err := s.dbManager.ListCollectors()
	if err != nil {
		return nil, fmt.Errorf("list collectors: %w", err)
	}

	views := make([]CollectorView, 0, len(collectorNames))
	for _, collectorName := range collectorNames {
		view, err := s.getCollectorBase(ctx, collectorName)
		if err != nil {
			slog.Error("Failed to get collector", "collector", collectorName, "error", err)
			continue
		}

		views = append(views, view)
	}

	return views, nil
}

func (s *Service) getCollectorBase(ctx context.Context, collectorName string) (CollectorView, error) {
	view := CollectorView{
		Name:        collectorName,
		IsConnected: false,
	}
	s.enrichCollectorConnection(&view)

	stats, err := s.ingestSvc.GetLatestStats(ctx, collectorName)
	if err == nil {
		view.Stats = stats
		view.StatsDisplay = formatStatsForDisplay(stats)
	}

	return view, nil
}

func (s *Service) GetAppHealth(ctx context.Context, user sharedtypes.User, collectorName, appName string, since time.Time) (AppHealth, error) {
	if err := s.requireAppAccess(ctx, user, collectorName, appName); err != nil {
		return AppHealth{}, err
	}

	health, err := withAppQueries(ctx, s, collectorName, appName, func(queries *db.Queries) (AppHealth, error) {
		return s.readAppHealth(ctx, queries, since, true)
	})
	if err != nil {
		return AppHealth{}, err
	}

	return health, nil
}

type GetStatsHistoryRequest struct {
	CollectorName string
	Since         time.Time
}

func (r GetStatsHistoryRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	return nil
}

func (s *Service) GetStatsHistory(ctx context.Context, user sharedtypes.User, req GetStatsHistoryRequest) ([]db.SystemStat, error) {
	if err := s.requireCollectorAccess(ctx, user, req.CollectorName); err != nil {
		return nil, err
	}

	return s.ingestSvc.GetStatsHistory(ctx, req.CollectorName, req.Since)
}

type GetAppStatsRequest struct {
	CollectorName string
	AppName       string
}

type AppStats struct {
	Total    int64
	Errors   int64
	Warnings int64
	Infos    int64
}

func (r GetAppStatsRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	return nil
}

func (s *Service) GetAppStats(ctx context.Context, user sharedtypes.User, req GetAppStatsRequest) (AppStats, error) {
	if err := s.requireAppAccess(ctx, user, req.CollectorName, req.AppName); err != nil {
		return AppStats{}, err
	}

	stats, err := withAppQueries(ctx, s, req.CollectorName, req.AppName, func(queries *db.Queries) (AppStats, error) {
		counts, err := queries.GetPercentiles(ctx, time.Time{})
		if err != nil {
			return AppStats{}, err
		}

		return AppStats{
			Total:    counts.Total,
			Errors:   counts.Errors,
			Warnings: counts.Warnings,
			Infos:    counts.Infos,
		}, nil
	})
	if err != nil {
		return AppStats{}, err
	}
	return stats, nil
}

type GetLogGroupsRequest struct {
	CollectorName string
	AppName       string
	Level         string
	Since         time.Time
	Offset        int64
	Limit         int64
}

type LogGroup struct {
	ID        int64
	Message   string
	Level     string
	Count     int64
	LatestAt  time.Time
	CreatedAt time.Time
}

func (r GetLogGroupsRequest) Validate() error {
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

func (s *Service) GetLogGroups(ctx context.Context, user sharedtypes.User, req GetLogGroupsRequest) ([]LogGroup, error) {
	if err := s.requireAppAccess(ctx, user, req.CollectorName, req.AppName); err != nil {
		return nil, err
	}

	groups, err := withAppQueries(ctx, s, req.CollectorName, req.AppName, func(queries *db.Queries) ([]LogGroup, error) {
		level := req.Level
		if level == "" {
			level = "%"
		}

		rows, err := queries.ListLogGroups(ctx, db.ListLogGroupsParams{
			Level:     level,
			CreatedAt: req.Since,
			Limit:     req.Limit,
			Offset:    req.Offset,
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
				LatestAt:  row.LatestAt,
				CreatedAt: row.CreatedAt,
			})
		}

		return groups, nil
	})
	if err != nil {
		return nil, err
	}
	return groups, nil
}

type GetLogDetailRequest struct {
	CollectorName string
	AppName       string
	GroupID       int64
	Offset        int64
	Limit         int64
}

type LogDetailResult struct {
	Group      LogGroup
	Logs       []db.GetLogsByGroupIDRow
	TotalCount int64
}

func (r GetLogDetailRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.GroupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	if r.Offset < 0 {
		return fmt.Errorf("offset must be non-negative")
	}
	if r.Limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}
	return nil
}

func (s *Service) GetLogDetail(ctx context.Context, user sharedtypes.User, req GetLogDetailRequest) (*LogDetailResult, error) {
	if err := s.requireAppAccess(ctx, user, req.CollectorName, req.AppName); err != nil {
		return nil, err
	}

	return withAppQueries(ctx, s, req.CollectorName, req.AppName, func(queries *db.Queries) (*LogDetailResult, error) {
		group, err := queries.GetLogGroupByID(ctx, req.GroupID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrLogNotFound
			}
			return nil, err
		}

		logs, err := queries.GetLogsByGroupID(ctx, db.GetLogsByGroupIDParams{
			GroupID: sql.NullInt64{Int64: req.GroupID, Valid: true},
			Limit:   req.Limit,
			Offset:  req.Offset,
		})
		if err != nil {
			return nil, err
		}

		totalCount, err := queries.CountLogsByGroupID(ctx, sql.NullInt64{Int64: req.GroupID, Valid: true})
		if err != nil {
			slog.Error("Failed to count logs", "error", err)
			totalCount = 0
		}

		return &LogDetailResult{
			Group: LogGroup{
				ID:        group.ID,
				Message:   group.Message,
				Level:     group.Level,
				Count:     group.Count,
				LatestAt:  group.LatestAt,
				CreatedAt: group.CreatedAt,
			},
			Logs:       logs,
			TotalCount: totalCount,
		}, nil
	})
}

type GetLogByIDRequest struct {
	CollectorName string
	AppName       string
	ID            int64
}

func (r GetLogByIDRequest) Validate() error {
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

func (s *Service) GetLogByID(ctx context.Context, user sharedtypes.User, req GetLogByIDRequest) (db.Log, error) {
	if err := s.requireAppAccess(ctx, user, req.CollectorName, req.AppName); err != nil {
		return db.Log{}, err
	}

	return withAppQueries(ctx, s, req.CollectorName, req.AppName, func(queries *db.Queries) (db.Log, error) {
		logEntry, err := queries.GetLogByID(ctx, req.ID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return db.Log{}, ErrLogNotFound
			}
			return db.Log{}, fmt.Errorf("get log: %w", err)
		}

		return logEntry, nil
	})
}

func (s *Service) GetLogVolumeTimeSeries(ctx context.Context, collectorName, appName string, since time.Time, granularity string) ([]db.GetLogVolumeTimeSeriesRow, error) {
	queries, err := s.queriesForApp(collectorName, appName)
	if err != nil {
		return nil, err
	}

	return queries.GetLogVolumeTimeSeries(ctx, db.GetLogVolumeTimeSeriesParams{
		Since:       since,
		Granularity: granularity,
	})
}

func withAppQueries[T any](ctx context.Context, s *Service, collectorName, appName string, fn func(*db.Queries) (T, error)) (T, error) {
	var zero T

	queries, err := s.queriesForApp(collectorName, appName)
	if err != nil {
		return zero, err
	}

	return fn(queries)
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

func (s *Service) requireCollectorAccess(ctx context.Context, user sharedtypes.User, collectorName string) error {
	exists, err := s.collectorExists(collectorName)
	if err != nil {
		return err
	}
	if !exists {
		return ErrCollectorNotFound
	}
	if user.IsAdmin {
		return nil
	}
	if user.ID == 0 {
		return ErrAccessDenied
	}

	allowedApps, err := s.userAppAccessSet(ctx, user)
	if err != nil {
		return err
	}
	if len(allowedApps[collectorName]) == 0 {
		return ErrAccessDenied
	}

	return nil
}

func (s *Service) requireAppAccess(ctx context.Context, user sharedtypes.User, collectorName, appName string) error {
	if err := s.requireCollectorAccess(ctx, user, collectorName); err != nil {
		return err
	}

	appNames, err := s.dbManager.ListApps(collectorName)
	if err != nil {
		return err
	}

	appExists := false
	for _, name := range appNames {
		if name == appName {
			appExists = true
			break
		}
	}
	if !appExists {
		return ErrAppNotFound
	}
	if user.IsAdmin {
		return nil
	}

	allowedApps, err := s.userAppAccessSet(ctx, user)
	if err != nil {
		return err
	}
	if _, ok := allowedApps[collectorName][appName]; !ok {
		return ErrAccessDenied
	}

	return nil
}

func (s *Service) userAppAccessSet(ctx context.Context, user sharedtypes.User) (map[string]map[string]struct{}, error) {
	if user.ID == 0 {
		return map[string]map[string]struct{}{}, nil
	}

	userApps, err := s.userSvc.GetApps(ctx, user.ID)
	if err != nil {
		return nil, err
	}

	userAppSet := make(map[string]map[string]struct{}, len(userApps))
	for _, app := range userApps {
		collectorApps := userAppSet[app.CollectorName]
		if collectorApps == nil {
			collectorApps = make(map[string]struct{})
			userAppSet[app.CollectorName] = collectorApps
		}
		collectorApps[app.AppName] = struct{}{}
	}

	return userAppSet, nil
}

func filterApps(apps []App, allowed map[string]struct{}) []App {
	if allowed == nil {
		return nil
	}

	filtered := make([]App, 0, len(apps))
	for _, app := range apps {
		if _, ok := allowed[app.Name]; ok {
			filtered = append(filtered, app)
		}
	}

	return filtered
}

func (s *Service) enrichCollectorConnection(view *CollectorView) {
	if trackerInfo, exists := s.tracker.Get(view.Name); exists {
		view.RemoteAddr = trackerInfo.RemoteAddr
		view.IsConnected = trackerInfo.IsConnected
		view.ConnectedAt = trackerInfo.ConnectedAt
		view.LastSeenAt = trackerInfo.LastSeenAt
	}
}

func (s *Service) loadCollectorApps(ctx context.Context, collectorName string, since time.Time, withBars bool, withHealth bool) ([]App, error) {
	appNames, err := s.dbManager.ListApps(collectorName)
	if err != nil {
		return nil, err
	}

	apps := make([]App, 0, len(appNames))
	for _, appName := range appNames {
		app := App{Name: appName}
		if withHealth {
			health, err := s.loadAppHealth(ctx, collectorName, appName, since, withBars)
			if err != nil {
				slog.Warn("Failed to load app health", "collector", collectorName, "app", appName, "error", err)
			} else {
				app.Health = health
			}
		}
		apps = append(apps, app)
	}

	return apps, nil
}

func formatStatsForDisplay(stats db.SystemStat) StatsDisplay {
	now := time.Now()
	age := now.Sub(stats.CreatedAt)

	nowY, nowM, nowD := now.Date()
	statY, statM, statD := stats.CreatedAt.Date()
	sameDate := nowY == statY && nowM == statM && nowD == statD

	var staleness string
	var formatted string

	if !sameDate {
		staleness = "old"
		formatted = stats.CreatedAt.Format("2006-01-02 15:04:05")
	} else if age > 30*time.Minute {
		staleness = "stale"
		formatted = stats.CreatedAt.Format("15:04:05")
	} else if age > 5*time.Minute {
		staleness = "warning"
		formatted = stats.CreatedAt.Format("15:04:05")
	} else {
		staleness = "fresh"
		formatted = stats.CreatedAt.Format("15:04:05")
	}

	return StatsDisplay{
		TimestampFormatted: formatted,
		Staleness:          staleness,
	}
}

func (s *Service) loadAppHealth(ctx context.Context, collectorName, appName string, since time.Time, withBars bool) (AppHealth, error) {
	return withAppQueries(ctx, s, collectorName, appName, func(queries *db.Queries) (AppHealth, error) {
		return s.readAppHealth(ctx, queries, since, withBars)
	})
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
		StartedAt: now,
		EndedAt:   sql.NullTime{Time: since, Valid: true},
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
	periodStart := period.StartedAt
	periodEnd := end
	if period.EndedAt.Valid {
		periodEnd = period.EndedAt.Time
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
