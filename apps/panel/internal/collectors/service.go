package collectors

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/libs/types"
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
	CreateAppDB(collectorName, appName string) (*sql.DB, error)
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

// SystemStats contains system statistics for a collector.
type SystemStats struct {
	CollectorName   string
	CpuUsagePercent float64
	MemTotalMb      int64
	MemUsedMb       int64
	MemAvailableMb  int64
	DiskTotalGb     int64
	DiskUsedGb      int64
	DiskAvailGb     int64
	CreatedAt       time.Time
}

// InsertSystemStats inserts system statistics for a collector.
// Creates the collector DB if it doesn't exist.
func (s *Service) InsertSystemStats(ctx context.Context, collector CollectorName, stats SystemStats) error {
	sqlDB, err := s.dbManager.CreateCollectorDB(collector.String())
	if err != nil {
		return err
	}
	queries := db.New(sqlDB)
	return queries.InsertSystemStats(ctx, db.InsertSystemStatsParams{
		CollectorName:   stats.CollectorName,
		CpuUsagePercent: stats.CpuUsagePercent,
		MemTotalMb:      stats.MemTotalMb,
		MemUsedMb:       stats.MemUsedMb,
		MemAvailableMb:  stats.MemAvailableMb,
		DiskTotalGb:     stats.DiskTotalGb,
		DiskUsedGb:      stats.DiskUsedGb,
		DiskAvailGb:     stats.DiskAvailGb,
		CreatedAt:       stats.CreatedAt,
	})
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

// Register creates all necessary databases for a collector and its apps.
// This should be called when a collector first connects/register.
func (s *Service) Register(collectorName string, apps []string) error {
	// Create collector DB for stats
	if _, err := s.dbManager.CreateCollectorDB(collectorName); err != nil {
		return err
	}

	// Create app databases for each app
	for _, appName := range apps {
		if appName == "" {
			continue
		}
		if _, err := s.dbManager.CreateAppDB(collectorName, appName); err != nil {
			return err
		}
	}

	return nil
}

// GetAppDB returns the database connection for the given collector/app.
// Assumes the database already exists (created during registration).
func (s *Service) GetAppDB(collectorName, appName string) (*sql.DB, error) {
	return s.dbManager.CreateAppDB(collectorName, appName)
}

// StoreLog stores a log entry for a specific app.
// It handles log group creation and insertion of the log entry.
func (s *Service) StoreLog(ctx context.Context, collectorName, appName string, entry types.LogEntry) error {
	sqlDB, err := s.dbManager.CreateAppDB(collectorName, appName)
	if err != nil {
		return err
	}
	queries := db.New(sqlDB)

	// Marshal extra context
	contextJSON := "{}"
	if len(entry.Extra) > 0 {
		if b, err := json.Marshal(entry.Extra); err == nil {
			contextJSON = string(b)
		}
	}

	// Get or create log group for this message and level
	logGroup, err := queries.GetOrCreateLogGroup(ctx, db.GetOrCreateLogGroupParams{
		Message: entry.Message,
		Level:   string(entry.Level),
	})
	if err != nil {
		return err
	}

	// Store log entry
	ts := entry.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	ts = ts.UTC()

	_, err = queries.InsertLog(ctx, db.InsertLogParams{
		TraceID:   entry.TraceID,
		Level:     string(entry.Level),
		Message:   entry.Message,
		Source:    entry.Source,
		Context:   sql.NullString{String: contextJSON, Valid: true},
		Raw:       entry.Raw,
		GroupID:   sql.NullInt64{Int64: logGroup.ID, Valid: true},
		CreatedAt: sql.NullTime{Time: ts, Valid: true},
	})
	return err
}

// StoreStartup stores a startup log entry for a specific app.
// Similar to StoreLog but uses "STARTUP" as the level.
func (s *Service) StoreStartup(ctx context.Context, collectorName, appName string, entry types.LogEntry) error {
	sqlDB, err := s.dbManager.CreateAppDB(collectorName, appName)
	if err != nil {
		return err
	}
	queries := db.New(sqlDB)

	// Marshal extra context
	contextJSON := "{}"
	if len(entry.Extra) > 0 {
		if b, err := json.Marshal(entry.Extra); err == nil {
			contextJSON = string(b)
		}
	}

	// Use "STARTUP" as the level for startup events
	level := "STARTUP"
	if entry.Level != "" {
		level = string(entry.Level)
	}

	// Get or create log group for this message and level
	logGroup, err := queries.GetOrCreateLogGroup(ctx, db.GetOrCreateLogGroupParams{
		Message: entry.Message,
		Level:   level,
	})
	if err != nil {
		return err
	}

	// Store startup entry
	ts := entry.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	ts = ts.UTC()

	_, err = queries.InsertLog(ctx, db.InsertLogParams{
		TraceID:   entry.TraceID,
		Level:     level,
		Message:   entry.Message,
		Source:    entry.Source,
		Context:   sql.NullString{String: contextJSON, Valid: true},
		Raw:       entry.Raw,
		GroupID:   sql.NullInt64{Int64: logGroup.ID, Valid: true},
		CreatedAt: sql.NullTime{Time: ts, Valid: true},
	})
	return err
}
