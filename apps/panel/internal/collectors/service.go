package collectors

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
)

// CollectorName is a type alias for collector names to make APIs clearer.
type CollectorName string

func (c CollectorName) String() string {
	return string(c)
}

// DBManager is the interface for dbmanager operations needed by Service.
type DBManager interface {
	ListCollectors() ([]string, error)
	ListApps(collector string) ([]string, error)
	GetCollectorDB(collectorName string) (*sql.DB, error)
	CreateCollectorDB(collectorName string) (*sql.DB, error)
}

// Service manages collector operations using dbmanager for all DB access.
type Service struct {
	dbManager DBManager
}

// New creates a new collector service.
func New(dbManager DBManager) *Service {
	return &Service{
		dbManager: dbManager,
	}
}

// List returns all collector names.
func (s *Service) List() ([]CollectorName, error) {
	names, err := s.dbManager.ListCollectors()
	if err != nil {
		return nil, err
	}

	collectors := make([]CollectorName, len(names))
	for i, name := range names {
		collectors[i] = CollectorName(name)
	}
	return collectors, nil
}

// ListApps returns all app names for a specific collector.
func (s *Service) ListApps(collector CollectorName) ([]string, error) {
	return s.dbManager.ListApps(collector.String())
}

// --- Stats Operations (using sqlc) ---

// InsertSystemStats inserts system statistics for a collector.
// Creates the collector DB if it doesn't exist.
func (s *Service) InsertSystemStats(ctx context.Context, collector CollectorName, p db.InsertSystemStatsParams) error {
	sqlDB, err := s.dbManager.CreateCollectorDB(collector.String())
	if err != nil {
		return err
	}
	queries := db.New(sqlDB)
	return queries.InsertSystemStats(ctx, p)
}

// GetLatestStats returns the latest system stats for a collector.
// Returns error if collector DB doesn't exist.
func (s *Service) GetLatestStats(ctx context.Context, collector CollectorName) (db.SystemStat, error) {
	sqlDB, err := s.dbManager.GetCollectorDB(collector.String())
	if err != nil {
		return db.SystemStat{}, err
	}
	queries := db.New(sqlDB)
	return queries.GetLatestStats(ctx)
}

// GetStatsHistory returns system stats history for a collector since a given time.
// Returns error if collector DB doesn't exist.
func (s *Service) GetStatsHistory(ctx context.Context, collector CollectorName, since time.Time) ([]db.SystemStat, error) {
	sqlDB, err := s.dbManager.GetCollectorDB(collector.String())
	if err != nil {
		return nil, err
	}
	queries := db.New(sqlDB)
	return queries.GetStatsHistory(ctx, since)
}

// DeleteOldStats removes stats older than the given time for all collectors.
// Skips collectors that don't have a DB yet.
func (s *Service) DeleteOldStats(ctx context.Context, before time.Time) error {
	collectors, err := s.List()
	if err != nil {
		return err
	}

	for _, collector := range collectors {
		sqlDB, err := s.dbManager.GetCollectorDB(collector.String())
		if err != nil {
			if errors.Is(err, dbmanager.ErrDBNotFound) {
				// Skip collectors without stats DB
				continue
			}
			return err
		}
		queries := db.New(sqlDB)
		if err := queries.DeleteOldStats(ctx, before); err != nil {
			return err
		}
	}
	return nil
}
