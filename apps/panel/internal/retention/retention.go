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

type Service struct {
	dbManager     *dbmanager.Manager
	ingestService *ingest.Service
	retentionDays int
}

func New(dbManager *dbmanager.Manager, ingestService *ingest.Service, retentionDays int) *Service {
	return &Service{
		dbManager:     dbManager,
		ingestService: ingestService,
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
	// List all ingests from filesystem
	ingestList, err := s.ingestService.ListCollectors()
	if err != nil {
		slog.Error("Failed to list ingests for cleanup", "error", err)
		return
	}

	cutoff := sql.NullTime{Time: time.Now().AddDate(0, 0, -s.retentionDays), Valid: true}

	// For each ingest, list apps and cleanup
	for _, collector := range ingestList {
		apps, err := s.ingestService.ListApps(collector)
		if err != nil {
			slog.Error("Failed to list apps for cleanup", "collector", collector, "error", err)
			continue
		}

		for _, appName := range apps {
			sqlDB, err := s.dbManager.GetAppDB(collector.String(), appName)
			if err != nil {
				if err == dbmanager.ErrDBNotFound {
					// Skip if DB doesn't exist
					continue
				}
				slog.Error("Failed to get DB for cleanup", "collector", collector, "app", appName, "error", err)
				continue
			}

			queries := db.New(sqlDB)
			err = queries.DeleteOldLogs(ctx, cutoff)
			if err != nil {
				slog.Error("Failed to cleanup logs", "collector", collector, "app", appName, "error", err)
				continue
			}

			slog.Debug("Cleaned up old logs", "collector", collector, "app", appName, "cutoff", cutoff.Time)
		}
	}

	// Cleanup old system stats
	s.cleanupSystemStats(ctx)
}

func (s *Service) cleanupSystemStats(ctx context.Context) {
	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)

	err := s.ingestService.DeleteOldStats(ctx, cutoff)
	if err != nil {
		slog.Error("Failed to cleanup system stats", "error", err)
		return
	}

	slog.Debug("Cleaned up old system stats", "cutoff", cutoff)
}
