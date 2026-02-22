package retention

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/collectors"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
)

type Service struct {
	dbManager     *dbmanager.Manager
	collectorService *collectors.Service
	retentionDays int
}

func New(dbManager *dbmanager.Manager, collectorService *collectors.Service, retentionDays int) *Service {
	return &Service{
		dbManager:     dbManager,
		collectorService: collectorService,
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
	// List all collectors from filesystem
	collectorList, err := s.collectorService.List()
	if err != nil {
		slog.Error("Failed to list collectors for cleanup", "error", err)
		return
	}

	cutoff := sql.NullTime{Time: time.Now().AddDate(0, 0, -s.retentionDays), Valid: true}

	// For each collector, list apps and cleanup
	for _, collector := range collectorList {
		apps, err := s.collectorService.ListApps(collector)
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

	err := s.collectorService.DeleteOldStats(ctx, cutoff)
	if err != nil {
		slog.Error("Failed to cleanup system stats", "error", err)
		return
	}

	slog.Debug("Cleaned up old system stats", "cutoff", cutoff)
}
