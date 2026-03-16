package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/services/notify"
	"tracer/libs/types"
)

var (
	ErrInvalidCollector = errors.New("invalid collector name")
	ErrInvalidApp       = errors.New("invalid app name")
	ErrInvalidEntry     = errors.New("invalid log entry")
	ErrInvalidStats     = errors.New("invalid stats data")
	ErrInvalidHealth    = errors.New("invalid health data")
)

// DBManager is the interface for dbmanager operations needed by Service.
type DBManager interface {
	ListCollectors() ([]string, error)
	ListApps(collector string) ([]string, error)
	GetCollectorDB(collectorName string) (db.DBTX, error)
	CreateCollectorDB(collectorName string) (db.DBTX, error)
	GetAppDB(collectorName, appName string) (db.DBTX, error)
	CreateAppDB(collectorName, appName string) (db.DBTX, error)
}

// Service manages ingest operations using dbmanager for all DB access.
type Service struct {
	dbManager      DBManager
	notifyChan     chan<- notify.Notification
	alertFormatter func(collectorName, appName string, logID int64) string
	sampleRate     float64
}

// New creates a new ingest service.
func New(dbManager DBManager, notifyChan chan<- notify.Notification, alertFormatter func(collectorName, appName string, logID int64) string, sampleRate float64) *Service {
	return &Service{
		dbManager:      dbManager,
		notifyChan:     notifyChan,
		alertFormatter: alertFormatter,
		sampleRate:     sampleRate,
	}
}

// ListCollectors returns all collector names.
func (s *Service) ListCollectors() ([]string, error) {
	return s.dbManager.ListCollectors()
}

// ListApps returns all app names for a specific collector.
func (s *Service) ListApps(collector string) ([]string, error) {
	return s.dbManager.ListApps(collector)
}

// --- Stats Operations (using sqlc) ---

// SaveSystemStatsRequest contains system statistics to save for a collector.
type SaveSystemStatsRequest struct {
	CollectorName string
	Stats         types.Stats
}

func (r SaveSystemStatsRequest) Validate() error {
	return domain.ValidateCollectorName(r.CollectorName)
}

// SaveSystemStats saves system statistics for a collector.
// Creates the collector DB if it doesn't exist.
func (s *Service) SaveSystemStats(ctx context.Context, req SaveSystemStatsRequest) error {
	dbtx, err := s.dbManager.GetCollectorDB(req.CollectorName)
	if err != nil {
		return err
	}
	queries := db.New(dbtx)
	return queries.InsertSystemStats(ctx, db.InsertSystemStatsParams{
		CollectorName:   req.CollectorName,
		CpuUsagePercent: req.Stats.CPUUsagePercent,
		MemTotalMb:      req.Stats.MemTotalMB,
		MemUsedMb:       req.Stats.MemUsedMB,
		MemAvailableMb:  req.Stats.MemAvailableMB,
		DiskTotalGb:     req.Stats.DiskTotalGB,
		DiskUsedGb:      req.Stats.DiskUsedGB,
		DiskAvailGb:     req.Stats.DiskAvailGB,
		CreatedAt:       req.Stats.Timestamp,
	})
}

// GetLatestStats returns the latest system stats for a collector.
// Returns error if collector DB doesn't exist.
func (s *Service) GetLatestStats(ctx context.Context, collector string) (db.SystemStat, error) {
	dbtx, err := s.dbManager.GetCollectorDB(collector)
	if err != nil {
		return db.SystemStat{}, err
	}
	queries := db.New(dbtx)
	return queries.GetLatestStats(ctx)
}

// GetStatsHistory returns system stats history for a collector since a given time.
// Returns error if collector DB doesn't exist.
func (s *Service) GetStatsHistory(ctx context.Context, collector string, since time.Time) ([]db.SystemStat, error) {
	dbtx, err := s.dbManager.GetCollectorDB(collector)
	if err != nil {
		return nil, err
	}
	queries := db.New(dbtx)
	return queries.GetStatsHistory(ctx, since)
}

// DeleteOldStats removes stats older than the given time for all collectors.
// Skips collectors that don't have a DB yet.
func (s *Service) DeleteOldStats(ctx context.Context, before time.Time) error {
	collectors, err := s.ListCollectors()
	if err != nil {
		return err
	}

	for _, collector := range collectors {
		dbtx, err := s.dbManager.GetCollectorDB(collector)
		if err != nil {
			if errors.Is(err, dbmanager.ErrDBNotFound) {
				// Skip collectors without stats DB
				continue
			}
			return err
		}
		queries := db.New(dbtx)
		if err := queries.DeleteOldStats(ctx, before); err != nil {
			return err
		}
	}
	return nil
}

type RegisterCollectorRequest struct {
	CollectorName string
	Apps          []string
}

func (r RegisterCollectorRequest) Validate() error {
	if err := domain.ValidateCollectorName(r.CollectorName); err != nil {
		return err
	}
	if len(r.Apps) == 0 {
		return fmt.Errorf("at least one app is required")
	}
	for _, app := range r.Apps {
		if err := domain.ValidateAppName(app); err != nil {
			return err
		}
	}
	return nil
}

// RegisterCollector creates all necessary databases for a collector and its apps.
// This should be called when a collector first connects/register.
func (s *Service) RegisterCollector(ctx context.Context, req RegisterCollectorRequest) error {
	// Create collector DB for stats
	if _, err := s.dbManager.CreateCollectorDB(req.CollectorName); err != nil {
		return err
	}

	// Create app databases for each app
	for _, appName := range req.Apps {
		if appName == "" {
			continue
		}
		if _, err := s.dbManager.CreateAppDB(req.CollectorName, appName); err != nil {
			return err
		}
	}

	return nil
}

type StoreLogRequest struct {
	CollectorName string
	AppName       string
	Entry         types.LogEntry
}

func (r StoreLogRequest) Validate() error {
	if err := domain.ValidateCollectorName(r.CollectorName); err != nil {
		return err
	}
	if err := domain.ValidateAppName(r.AppName); err != nil {
		return err
	}
	return nil
}

// StoreLog stores a log entry for a specific app.
// It handles log group creation and insertion of the log entry.
// Returns the log ID for potential notifications.
// Includes sampling logic and notification sending for errors.
func (s *Service) StoreLog(ctx context.Context, req StoreLogRequest) (int64, error) {
	// Check if this is an error level log
	isError := req.Entry.Level == types.LevelError

	// Sampling logic: always persist errors, sample others
	if !isError && rand.Float64() >= s.sampleRate {
		return 0, nil // Skipped by sampling
	}

	dbtx, err := s.dbManager.GetAppDB(req.CollectorName, req.AppName)
	if err != nil {
		return 0, err
	}
	queries := db.New(dbtx)

	// Marshal extra context
	contextJSON := "{}"
	if len(req.Entry.Extra) > 0 {
		if b, err := json.Marshal(req.Entry.Extra); err == nil {
			contextJSON = string(b)
		}
	}

	// Get or create log group for this message and level
	logGroup, err := queries.GetOrCreateLogGroup(ctx, db.GetOrCreateLogGroupParams{
		Message: req.Entry.Message,
		Level:   string(req.Entry.Level),
	})
	if err != nil {
		return 0, err
	}

	// Store log entry
	ts := req.Entry.Timestamp

	log, err := queries.InsertLog(ctx, db.InsertLogParams{
		TraceID:   sql.NullString{String: req.Entry.TraceID, Valid: true},
		Level:     string(req.Entry.Level),
		Message:   req.Entry.Message,
		Context:   sql.NullString{String: contextJSON, Valid: true},
		GroupID:   sql.NullInt64{Int64: logGroup.ID, Valid: true},
		CreatedAt: ts,
	})
	if err != nil {
		return 0, err
	}

	logID := log.ID

	// Send notification for errors
	if isError && s.notifyChan != nil {
		s.sendErrorNotification(req, logID, logGroup.ID)
	}

	return logID, nil
}

type StoreLifecycleRequest struct {
	CollectorName string
	AppName       string
	Kind          types.LifecycleKind
	RawLine       string
	Timestamp     time.Time
}

func (r StoreLifecycleRequest) Validate() error {
	if err := domain.ValidateCollectorName(r.CollectorName); err != nil {
		return err
	}
	if err := domain.ValidateAppName(r.AppName); err != nil {
		return err
	}
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if r.RawLine == "" {
		return fmt.Errorf("raw line is required")
	}
	return nil
}

// StoreLifecycle stores lifecycle events in a dedicated app-level table.
func (s *Service) StoreLifecycle(ctx context.Context, req StoreLifecycleRequest) error {
	dbtx, err := s.dbManager.GetAppDB(req.CollectorName, req.AppName)
	if err != nil {
		return err
	}
	queries := db.New(dbtx)

	_, err = queries.InsertLifecycleEvent(ctx, db.InsertLifecycleEventParams{
		Kind:      string(req.Kind),
		RawLine:   req.RawLine,
		CreatedAt: req.Timestamp,
	})
	if err != nil {
		return err
	}

	if s.notifyChan != nil {
		select {
		case s.notifyChan <- notify.Notification{
			Type:          notify.TypeLifecycle,
			CollectorName: req.CollectorName,
			AppName:       req.AppName,
			Message:       string(req.Kind),
		}:
		default:
			slog.Error("Notification channel full, dropping lifecycle notification")
		}
	}

	return nil
}

type StoreHealthRequest struct {
	CollectorName string
	AppName       string
	Status        types.HealthStatus
	Time          time.Time
}

func (r StoreHealthRequest) Validate() error {
	if err := domain.ValidateCollectorName(r.CollectorName); err != nil {
		return err
	}
	if err := domain.ValidateAppName(r.AppName); err != nil {
		return err
	}
	switch r.Status {
	case types.HealthStatusUp, types.HealthStatusDown, types.HealthStatusUnknown:
	default:
		return fmt.Errorf("%w: invalid status %q", ErrInvalidHealth, r.Status)
	}
	if r.Time.IsZero() {
		return fmt.Errorf("%w: time is required", ErrInvalidHealth)
	}
	return nil
}

func (s *Service) SaveHealthPeriod(ctx context.Context, req StoreHealthRequest) error {
	dbtx, err := s.dbManager.GetAppDB(req.CollectorName, req.AppName)
	if err != nil {
		return err
	}
	queries := db.New(dbtx)
	now := req.Time

	current, err := queries.GetOpenHealthPeriod(ctx)
	switch {
	case err == nil:
		if current.Status == string(req.Status) {
			return nil
		}
		if err := queries.CloseHealthPeriod(ctx, db.CloseHealthPeriodParams{
			EndedAt: sql.NullTime{Time: now, Valid: true},
			ID:      current.ID,
		}); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}

	err = queries.SaveHealthPeriod(ctx, db.SaveHealthPeriodParams{
		Status:    string(req.Status),
		StartedAt: now,
		EndedAt:   sql.NullTime{},
	})
	return err
}

// sendErrorNotification sends a notification for error logs
func (s *Service) sendErrorNotification(req StoreLogRequest, logID, groupID int64) {
	// Build the alert message with markdown link
	alertMessage := fmt.Sprintf("```%s```", req.Entry.Message)
	if s.alertFormatter != nil {
		// Include log ID and group ID in URL
		logsURL := s.alertFormatter(req.CollectorName, req.AppName, logID)
		if logsURL != "" {
			alertMessage += fmt.Sprintf("\n\n[view log](%s)", logsURL)
		}
	}

	select {
	case s.notifyChan <- notify.Notification{
		Type:          notify.TypeAlert,
		CollectorName: req.CollectorName,
		AppName:       req.AppName,
		Message:       alertMessage,
	}:
	default:
		slog.Error("Notification channel full, dropping notification")
	}
}
