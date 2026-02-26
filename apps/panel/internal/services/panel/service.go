package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/permissions"
	sharedtypes "tracer/libs/types"
)

var (
	ErrAppNotFound      = errors.New("app not found")
	ErrLogNotFound      = errors.New("log not found")
	ErrAccessDenied     = errors.New("access denied")
	ErrInvalidCollector = errors.New("invalid collector name")
	ErrInvalidApp       = errors.New("invalid app name")
	ErrInvalidGroupID   = errors.New("invalid group ID")
	ErrInvalidLogID     = errors.New("invalid log ID")
	ErrInvalidOffset    = errors.New("invalid offset")
	ErrInvalidLimit     = errors.New("invalid limit")
)

type Service struct {
	dbManager   *dbmanager.Manager
	permissions *permissions.Service
	ingestSvc   *ingest.Service
	tracker     *ingest.Tracker
}

func New(dbManager *dbmanager.Manager, permissions *permissions.Service, ingestSvc *ingest.Service, tracker *ingest.Tracker) *Service {
	return &Service{
		dbManager:   dbManager,
		permissions: permissions,
		ingestSvc:   ingestSvc,
		tracker:     tracker,
	}
}

type AppStats struct {
	Total    int64
	Errors   int64
	Warnings int64
	Infos    int64
}

type LogDetailResult struct {
	Group      LogGroup
	Logs       []db.GetLogsByGroupIDRow
	TotalCount int64
}

type LogGroupsResult struct {
	Groups any
}

// StatsDisplay holds formatted stats data for UI rendering.
type StatsDisplay struct {
	TimestampFormatted string
	Staleness          string // "fresh", "warning", "stale", "old"
}

// Request structs for service methods

type GetCollectorDetailRequest struct {
	User          sharedtypes.User
	CollectorName string
}

func (r GetCollectorDetailRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	return nil
}

type GetStatsHistoryRequest struct {
	User          sharedtypes.User
	CollectorName string
	Since         time.Time
}

func (r GetStatsHistoryRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	return nil
}

type GetAppAnalyticsRequest struct {
	User          sharedtypes.User
	CollectorName string
	AppName       string
}

func (r GetAppAnalyticsRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	return nil
}

type GetLogGroupsRequest struct {
	User          sharedtypes.User
	CollectorName string
	AppName       string
	Level         string
	Since         time.Time
	Offset        int64
	Limit         int64
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

type GetLogDetailRequest struct {
	User          sharedtypes.User
	CollectorName string
	AppName       string
	GroupID       int64
	Offset        int64
	Limit         int64
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

type GetLogByIDRequest struct {
	User          sharedtypes.User
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

// CollectorView provides a complete view of a collector for display,
// with data pre-filtered according to user permissions.
type CollectorView struct {
	Name         string
	RemoteAddr   string
	IsConnected  bool
	ConnectedAt  time.Time
	LastSeenAt   time.Time
	Apps         []string // already filtered by user permissions
	Stats        db.SystemStat
	StatsDisplay StatsDisplay
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

// FIXME: Stop db handjob
func (s *Service) GetAppAnalytics(ctx context.Context, req GetAppAnalyticsRequest) (AppStats, error) {
	var stats AppStats
	hasAccess, err := s.permissions.CanAccessApp(ctx, req.User, req.AppName)
	if err != nil {
		return stats, err
	}
	if !hasAccess {
		return stats, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return stats, err
	}

	totalCount, err := queries.CountLogGroups(ctx)
	if err != nil {
		slog.Error("Failed to count log groups", "error", err)
		totalCount = 0
	}
	errorCount, err := queries.CountLogGroupsByLevel(ctx, "error")
	if err != nil {
		slog.Error("Failed to count error groups", "error", err)
		errorCount = 0
	}
	warnCount, err := queries.CountLogGroupsByLevel(ctx, "warn")
	if err != nil {
		slog.Error("Failed to count warn groups", "error", err)
		warnCount = 0
	}
	infoCount, err := queries.CountLogGroupsByLevel(ctx, "info")
	if err != nil {
		slog.Error("Failed to count info groups", "error", err)
		infoCount = 0
	}

	return AppStats{
		Total:    totalCount,
		Errors:   errorCount,
		Warnings: warnCount,
		Infos:    infoCount,
	}, nil
}

func (s *Service) GetLogDetail(ctx context.Context, req GetLogDetailRequest) (*LogDetailResult, error) {
	hasAccess, err := s.permissions.CanAccessApp(ctx, req.User, req.AppName)
	if err != nil {
		return nil, err
	}
	if !hasAccess {
		return nil, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return nil, err
	}

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
}

type LogGroup struct {
	ID        int64
	Message   string
	Level     string
	Count     int64
	LatestAt  time.Time
	CreatedAt time.Time
}

func (s *Service) GetLogGroups(ctx context.Context, req GetLogGroupsRequest) ([]LogGroup, error) {
	var groups []LogGroup

	hasAccess, err := s.permissions.CanAccessApp(ctx, req.User, req.AppName)
	if err != nil {
		return groups, err
	}
	if !hasAccess {
		return groups, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return groups, err
	}

	if req.Level == "" {
		req.Level = "%"
	}
	gRows, err := queries.ListLogGroups(ctx, db.ListLogGroupsParams{
		Level:     req.Level,
		CreatedAt: req.Since,
		Limit:     req.Limit,
		Offset:    req.Offset,
	})
	if err != nil {
		return nil, err
	}

	// TODO: Map to []LogGroup
	for _, g := range gRows {
		groups = append(groups, LogGroup{
			ID:        g.ID,
			Message:   g.Message,
			Level:     g.Level,
			Count:     g.Count,
			LatestAt:  g.LatestAt,
			CreatedAt: g.CreatedAt,
		})
	}

	return groups, nil
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

func (s *Service) GetLogByID(ctx context.Context, req GetLogByIDRequest) (db.Log, error) {
	hasAccess, err := s.permissions.CanAccessApp(ctx, req.User, req.AppName)
	if err != nil {
		return db.Log{}, err
	}
	if !hasAccess {
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

// GetCollectorsView returns all collectors with their data filtered for the given user.
func (s *Service) GetCollectorsView(ctx context.Context, user sharedtypes.User) ([]CollectorView, error) {
	collectorList := s.tracker.List()
	views := make([]CollectorView, 0, len(collectorList))

	for _, c := range collectorList {
		hasAccess, err := s.permissions.CanAccessCollector(ctx, user, c.Name)
		if err != nil {
			return nil, err
		}
		if !hasAccess {
			continue
		}

		view := CollectorView{
			Name:        c.Name,
			RemoteAddr:  c.RemoteAddr,
			IsConnected: c.IsConnected,
			ConnectedAt: c.ConnectedAt,
			LastSeenAt:  c.LastSeenAt,
		}

		// Filter apps by user access
		filteredApps, err := s.permissions.FilterAppsByAccess(ctx, user, c.Apps)
		if err != nil {
			return nil, err
		}
		view.Apps = filteredApps

		// Get latest stats
		stats, err := s.ingestSvc.GetLatestStats(ctx, c.Name)
		if err == nil {
			view.Stats = stats
			view.StatsDisplay = formatStatsForDisplay(stats)
		}

		views = append(views, view)
	}

	return views, nil
}

// GetCollectorDetail returns a single collector view with access check.
// Returns ErrAccessDenied if the user doesn't have access to the collector.
func (s *Service) GetCollectorDetail(ctx context.Context, req GetCollectorDetailRequest) (CollectorView, error) {
	collector, exists := s.tracker.Get(req.CollectorName)
	if !exists {
		return CollectorView{}, ErrAppNotFound
	}

	hasAccess, err := s.permissions.CanAccessCollector(ctx, req.User, req.CollectorName)
	if err != nil {
		return CollectorView{}, err
	}
	if !hasAccess {
		return CollectorView{}, ErrAccessDenied
	}

	view := CollectorView{
		Name:        collector.Name,
		RemoteAddr:  collector.RemoteAddr,
		IsConnected: collector.IsConnected,
		ConnectedAt: collector.ConnectedAt,
		LastSeenAt:  collector.LastSeenAt,
	}

	// Filter apps by user access
	filteredApps, err := s.permissions.FilterAppsByAccess(ctx, req.User, collector.Apps)
	if err != nil {
		return CollectorView{}, err
	}
	view.Apps = filteredApps

	// Get latest stats
	stats, err := s.ingestSvc.GetLatestStats(ctx, collector.Name)
	if err == nil {
		view.Stats = stats
		view.StatsDisplay = formatStatsForDisplay(stats)
	}

	return view, nil
}

// FilterCollectorApps filters the given apps by user access permissions.
func (s *Service) FilterCollectorApps(ctx context.Context, user sharedtypes.User, apps []string) ([]string, error) {
	return s.permissions.FilterAppsByAccess(ctx, user, apps)
}

// CanAccessApp checks if the user has access to a specific app.
func (s *Service) CanAccessApp(ctx context.Context, user sharedtypes.User, appName string) (bool, error) {
	return s.permissions.CanAccessApp(ctx, user, appName)
}

// GetStatsHistory returns stats history for a collector.
// This is a wrapper around ingest service for now.
func (s *Service) GetStatsHistory(ctx context.Context, req GetStatsHistoryRequest) ([]db.SystemStat, error) {
	hasAccess, err := s.permissions.CanAccessCollector(ctx, req.User, req.CollectorName)
	if err != nil {
		return nil, err
	}
	if !hasAccess {
		return nil, ErrAccessDenied
	}
	return s.ingestSvc.GetStatsHistory(ctx, req.CollectorName, req.Since)
}

// formatStatsForDisplay formats system stats for UI display.
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
