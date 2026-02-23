package panel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/apps/panel/internal/services/ingest"
	"github.com/ai-shift/tracer/apps/panel/internal/services/permissions"
	sharedtypes "github.com/ai-shift/tracer/libs/types"
)

var (
	ErrAppNotFound  = errors.New("app not found")
	ErrLogNotFound  = errors.New("log not found")
	ErrAccessDenied = errors.New("access denied")
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
	Group      db.LogGroup
	Logs       []db.GetLogsByGroupIDRow
	TotalCount int64
}

type LogGroupsResult struct {
	Groups     any
	TotalCount int64
	ErrorCount int64
	WarnCount  int64
	InfoCount  int64
}

// StatsDisplay holds formatted stats data for UI rendering.
type StatsDisplay struct {
	TimestampFormatted string
	Staleness          string // "fresh", "warning", "stale", "old"
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

func (s *Service) GetAppAnalytics(ctx context.Context, user sharedtypes.User, collectorName, appName string) (*AppStats, []db.ListLogGroupsRow, error) {
	hasAccess, err := s.permissions.CanAccessApp(ctx, user, appName)
	if err != nil {
		return nil, nil, err
	}
	if !hasAccess {
		return nil, nil, ErrAccessDenied
	}

	queries, err := s.queriesForApp(collectorName, appName)
	if err != nil {
		return nil, nil, err
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

	grouped, err := queries.ListLogGroups(ctx, db.ListLogGroupsParams{
		Limit:  20,
		Offset: 0,
	})
	if err != nil {
		return nil, nil, err
	}

	return &AppStats{
		Total:    totalCount,
		Errors:   errorCount,
		Warnings: warnCount,
		Infos:    infoCount,
	}, grouped, nil
}

func (s *Service) GetLogDetail(ctx context.Context, user sharedtypes.User, collectorName, appName string, groupID, offset, limit int64) (*LogDetailResult, error) {
	hasAccess, err := s.permissions.CanAccessApp(ctx, user, appName)
	if err != nil {
		return nil, err
	}
	if !hasAccess {
		return nil, ErrAccessDenied
	}

	queries, err := s.queriesForApp(collectorName, appName)
	if err != nil {
		return nil, err
	}

	group, err := queries.GetLogGroupByID(ctx, groupID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrLogNotFound
		}
		return nil, err
	}

	logs, err := queries.GetLogsByGroupID(ctx, db.GetLogsByGroupIDParams{
		GroupID: sql.NullInt64{Int64: groupID, Valid: true},
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		return nil, err
	}

	totalCount, err := queries.CountLogsByGroupID(ctx, sql.NullInt64{Int64: groupID, Valid: true})
	if err != nil {
		slog.Error("Failed to count logs", "error", err)
		totalCount = 0
	}

	return &LogDetailResult{
		Group:      group,
		Logs:       logs,
		TotalCount: totalCount,
	}, nil
}

func (s *Service) GetLogGroups(ctx context.Context, user sharedtypes.User, collectorName, appName, level string, offset, limit int64) (*LogGroupsResult, error) {
	hasAccess, err := s.permissions.CanAccessApp(ctx, user, appName)
	if err != nil {
		return nil, err
	}
	if !hasAccess {
		return nil, ErrAccessDenied
	}

	queries, err := s.queriesForApp(collectorName, appName)
	if err != nil {
		return nil, err
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

	var groups any
	if level != "" {
		groups, err = queries.ListLogGroupsByLevel(ctx, db.ListLogGroupsByLevelParams{
			LOWER:  level,
			Limit:  limit,
			Offset: offset,
		})
	} else {
		groups, err = queries.ListLogGroups(ctx, db.ListLogGroupsParams{
			Limit:  limit,
			Offset: offset,
		})
	}
	if err != nil {
		return nil, err
	}

	return &LogGroupsResult{
		Groups:     groups,
		TotalCount: totalCount,
		ErrorCount: errorCount,
		WarnCount:  warnCount,
		InfoCount:  infoCount,
	}, nil
}

func (s *Service) GetLogVolumeTimeSeries(ctx context.Context, collectorName, appName string, since time.Time, granularity string) ([]db.GetLogVolumeTimeSeriesRow, error) {
	queries, err := s.queriesForApp(collectorName, appName)
	if err != nil {
		return nil, err
	}

	return queries.GetLogVolumeTimeSeries(ctx, db.GetLogVolumeTimeSeriesParams{
		Since:       sql.NullTime{Time: since, Valid: true},
		Granularity: granularity,
	})
}

func (s *Service) GetLogByID(ctx context.Context, user sharedtypes.User, collectorName, appName string, id int64) (db.Log, error) {
	hasAccess, err := s.permissions.CanAccessApp(ctx, user, appName)
	if err != nil {
		return db.Log{}, err
	}
	if !hasAccess {
		return db.Log{}, ErrAccessDenied
	}

	queries, err := s.queriesForApp(collectorName, appName)
	if err != nil {
		return db.Log{}, err
	}

	logEntry, err := queries.GetLogByID(ctx, id)
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
		stats, err := s.ingestSvc.GetLatestStats(ctx, ingest.CollectorName(c.Name))
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
func (s *Service) GetCollectorDetail(ctx context.Context, user sharedtypes.User, collectorName string) (CollectorView, error) {
	collector, exists := s.tracker.Get(collectorName)
	if !exists {
		return CollectorView{}, ErrAppNotFound
	}

	hasAccess, err := s.permissions.CanAccessCollector(ctx, user, collectorName)
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
	filteredApps, err := s.permissions.FilterAppsByAccess(ctx, user, collector.Apps)
	if err != nil {
		return CollectorView{}, err
	}
	view.Apps = filteredApps

	// Get latest stats
	stats, err := s.ingestSvc.GetLatestStats(ctx, ingest.CollectorName(collector.Name))
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
func (s *Service) GetStatsHistory(ctx context.Context, user sharedtypes.User, collectorName string, since time.Time) ([]db.SystemStat, error) {
	hasAccess, err := s.permissions.CanAccessCollector(ctx, user, collectorName)
	if err != nil {
		return nil, err
	}
	if !hasAccess {
		return nil, ErrAccessDenied
	}
	return s.ingestSvc.GetStatsHistory(ctx, ingest.CollectorName(collectorName), since)
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
