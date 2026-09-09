package retention

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/ingest"
)

// RetentionConfig provides runtime access to retention-related config values.
type RetentionConfig interface {
	GetRetentionDays() int
}

type Service struct {
	dbManager     *dbmanager.Manager
	ingestService *ingest.Service
	cfg           RetentionConfig
}

func New(dbManager *dbmanager.Manager, ingestService *ingest.Service, cfg RetentionConfig) *Service {
	return &Service{
		dbManager:     dbManager,
		ingestService: ingestService,
		cfg:           cfg,
	}
}

func (s *Service) Start(ctx context.Context) {
	slog.Info("Retention service started", "retention_days", s.cfg.GetRetentionDays())

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
	// List all ingests from filesystem
	ingestList, err := s.ingestService.ListCollectors()
	if err != nil {
		slog.Error("Failed to list ingests for cleanup", "error", err)
		return
	}

	cutoff := time.Now().AddDate(0, 0, -s.cfg.GetRetentionDays())

	// For each ingest, list apps and cleanup
	for _, collector := range ingestList {
		apps, err := s.ingestService.ListApps(collector)
		if err != nil {
			slog.Error("Failed to list apps for cleanup", "collector", collector, "error", err)
			continue
		}

		for _, appName := range apps {
			handle, err := s.dbManager.GetAppDB(collector, appName)
			if err != nil {
				if err == dbmanager.ErrDBNotFound {
					// Skip if DB doesn't exist
					continue
				}
				slog.Error("Failed to get DB for cleanup", "collector", collector, "app", appName, "error", err)
				continue
			}

			if err := handle.Write(ctx, func(tx db.DBTX) error {
				q := db.New(tx)
				if err := q.DeleteOldLogs(ctx, cutoff.Unix()); err != nil {
					return err
				}
				// Recalculate counters after deletion to keep them accurate
				if err := q.RecalculateLogGroupCounters(ctx); err != nil {
					return err
				}
				if err := q.DeleteOldHealthChecks(ctx, cutoff.Unix()); err != nil {
					return err
				}
				return q.DeleteOldHealthPeriods(ctx, sql.NullInt64{Int64: cutoff.Unix(), Valid: true})
			}); err != nil {
				slog.Error("Failed to cleanup app data", "collector", collector, "app", appName, "error", err)
				continue
			}

			slog.Debug("Cleaned up old app data", "collector", collector, "app", appName, "cutoff", cutoff)
		}
	}

	// Cleanup old system stats
	s.cleanupSystemStats(ctx)
}

func (s *Service) cleanupSystemStats(ctx context.Context) {
	cutoff := time.Now().AddDate(0, 0, -s.cfg.GetRetentionDays())

	err := s.ingestService.DeleteOldStats(ctx, cutoff)
	if err != nil {
		slog.Error("Failed to cleanup system stats", "error", err)
		return
	}

	slog.Debug("Cleaned up old system stats", "cutoff", cutoff)
}
