package retention

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/collectordb"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
)

type Service struct {
	dbManager     *dbmanager.Manager
	collectorDB   *collectordb.Manager
	retentionDays int
}

func New(dbManager *dbmanager.Manager, collectorDB *collectordb.Manager, retentionDays int) *Service {
	return &Service{
		dbManager:     dbManager,
		collectorDB:   collectorDB,
		retentionDays: retentionDays,
	}
}

func (s *Service) Start(ctx context.Context) {
	slog.Info("Retention service started", "retention_days", s.retentionDays)

	// Run immediately on start
	s.runCleanup(ctx)

	// Then run daily
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Retention service stopping")
			return
		case <-ticker.C:
			s.runCleanup(ctx)
		}
	}
}

func (s *Service) runCleanup(ctx context.Context) {
	instances, err := s.dbManager.ListAppInstances()
	if err != nil {
		slog.Error("Failed to list app instances for cleanup", "error", err)
		return
	}

	cutoff := sql.NullTime{Time: time.Now().AddDate(0, 0, -s.retentionDays), Valid: true}

	for _, instance := range instances {
		sqlDB, err := s.dbManager.GetDB(instance.CollectorName, instance.AppName)
		if err != nil {
			slog.Error("Failed to get DB for cleanup", "collector_name", instance.CollectorName, "app_name", instance.AppName, "error", err)
			continue
		}

		queries := db.New(sqlDB)
		err = queries.DeleteOldLogs(ctx, cutoff)
		if err != nil {
			slog.Error("Failed to cleanup logs", "collector_name", instance.CollectorName, "app_name", instance.AppName, "error", err)
			continue
		}

		slog.Debug("Cleaned up old logs", "collector_name", instance.CollectorName, "app_name", instance.AppName, "cutoff", cutoff.Time)
	}

	// Cleanup old system stats
	s.cleanupSystemStats(ctx)
}

func (s *Service) cleanupSystemStats(ctx context.Context) {
	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)

	err := s.collectorDB.DeleteOldStats(ctx, cutoff)
	if err != nil {
		slog.Error("Failed to cleanup system stats", "error", err)
		return
	}

	slog.Debug("Cleaned up old system stats", "cutoff", cutoff)
}
