package panelread

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
)

var (
	ErrAppNotFound = errors.New("app not found")
	ErrLogNotFound = errors.New("log not found")
)

type Service struct {
	dbManager *dbmanager.Manager
}

func New(dbManager *dbmanager.Manager) *Service {
	return &Service{dbManager: dbManager}
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

func (s *Service) GetAppAnalytics(ctx context.Context, collectorName, appName string) (*AppStats, []db.ListLogGroupsRow, error) {
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

func (s *Service) GetLogDetail(ctx context.Context, collectorName, appName string, groupID, offset, limit int64) (*LogDetailResult, error) {
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

func (s *Service) GetLogGroups(ctx context.Context, collectorName, appName, level string, offset, limit int64) (*LogGroupsResult, error) {
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

func (s *Service) GetLogByID(ctx context.Context, collectorName, appName string, id int64) (db.Log, error) {
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
