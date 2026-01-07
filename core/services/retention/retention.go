package retention

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/ai-shift/tracer/core/services/auth"
	"github.com/ai-shift/tracer/core/services/dbmanager"
)

type Service struct {
	dbManager     *dbmanager.Manager
	authService   *auth.Service
	retentionDays int
}

func New(dbManager *dbmanager.Manager, authService *auth.Service, retentionDays int) *Service {
	return &Service{
		dbManager:     dbManager,
		authService:   authService,
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
	apps, err := s.dbManager.ListApps()
	if err != nil {
		slog.Error("Failed to list apps for cleanup", "error", err)
		return
	}

	cutoff := sql.NullTime{Time: time.Now().AddDate(0, 0, -s.retentionDays), Valid: true}

	for _, appName := range apps {
		queries, err := s.dbManager.GetQueries(appName)
		if err != nil {
			slog.Error("Failed to get queries for cleanup", "app_name", appName, "error", err)
			continue
		}

		err = queries.DeleteOldLogs(ctx, cutoff)
		if err != nil {
			slog.Error("Failed to cleanup logs", "app_name", appName, "error", err)
			continue
		}

		slog.Debug("Cleaned up old logs", "app_name", appName, "cutoff", cutoff.Time)
	}

	// Cleanup old system stats
	s.cleanupSystemStats(ctx)
}

func (s *Service) cleanupSystemStats(ctx context.Context) {
	cutoff := time.Now().AddDate(0, 0, -s.retentionDays)

	err := s.authService.DeleteOldStats(ctx, cutoff)
	if err != nil {
		slog.Error("Failed to cleanup system stats", "error", err)
		return
	}

	slog.Debug("Cleaned up old system stats", "cutoff", cutoff)
}
