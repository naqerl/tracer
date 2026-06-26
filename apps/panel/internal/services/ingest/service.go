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
	GetCollectorDB(collectorName string) (*dbmanager.DBHandle, error)
	CreateCollectorDB(collectorName string) (*dbmanager.DBHandle, error)
	GetAppDB(collectorName, appName string) (*dbmanager.DBHandle, error)
	CreateAppDB(collectorName, appName string) (*dbmanager.DBHandle, error)
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
	handle, err := s.dbManager.GetCollectorDB(req.CollectorName)
	if err != nil {
		return err
	}
	return handle.Write(ctx, func(tx db.DBTX) error {
		return db.New(tx).InsertSystemStats(ctx, db.InsertSystemStatsParams{
			CollectorName:   req.CollectorName,
			CpuUsagePercent: req.Stats.CPUUsagePercent,
			MemTotalMb:      req.Stats.MemTotalMB,
			MemUsedMb:       req.Stats.MemUsedMB,
			MemAvailableMb:  req.Stats.MemAvailableMB,
			DiskTotalGb:     req.Stats.DiskTotalGB,
			DiskUsedGb:      req.Stats.DiskUsedGB,
			DiskAvailGb:     req.Stats.DiskAvailGB,
			CreatedAt:       req.Stats.Timestamp.Unix(),
		})
	})
}

// GetLatestStats returns the latest system stats for a collector.
// Returns error if collector DB doesn't exist.
func (s *Service) GetLatestStats(ctx context.Context, collector string) (db.SystemStat, error) {
	handle, err := s.dbManager.GetCollectorDB(collector)
	if err != nil {
		return db.SystemStat{}, err
	}
	queries := db.New(handle.Read())
	return queries.GetLatestStats(ctx)
}

// GetStatsHistory returns system stats history for a collector since a given time.
// Returns error if collector DB doesn't exist.
func (s *Service) GetStatsHistory(ctx context.Context, collector string, since time.Time) ([]db.SystemStat, error) {
	handle, err := s.dbManager.GetCollectorDB(collector)
	if err != nil {
		return nil, err
	}
	queries := db.New(handle.Read())
	return queries.GetStatsHistory(ctx, since.UTC().Unix())
}

// DeleteOldStats removes stats older than the given time for all collectors.
// Skips collectors that don't have a DB yet.
func (s *Service) DeleteOldStats(ctx context.Context, before time.Time) error {
	collectors, err := s.ListCollectors()
	if err != nil {
		return err
	}

	for _, collector := range collectors {
		handle, err := s.dbManager.GetCollectorDB(collector)
		if err != nil {
			if errors.Is(err, dbmanager.ErrDBNotFound) {
				// Skip collectors without stats DB
				continue
			}
			return err
		}
		if err := handle.Write(ctx, func(tx db.DBTX) error {
			return db.New(tx).DeleteOldStats(ctx, before.UTC().Unix())
		}); err != nil {
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

const (
	maxMessageLength     = 10000
	maxTraceIDLength     = 128
	maxExtraEntries      = 100
	maxExtraKeyLength    = 256
	maxExtraValueLength  = 4096
	maxContextJSONLength = 100000
)

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

	// Validate log entry fields
	switch r.Entry.Level {
	case types.LevelDebug, types.LevelInfo, types.LevelWarn, types.LevelError:
		// valid
	default:
		return fmt.Errorf("invalid log level: %q", r.Entry.Level)
	}

	if r.Entry.Timestamp.IsZero() {
		return fmt.Errorf("log timestamp is required")
	}

	if r.Entry.Message == "" {
		return fmt.Errorf("log message is required")
	}
	if len(r.Entry.Message) > maxMessageLength {
		return fmt.Errorf("log message too long (max %d characters)", maxMessageLength)
	}

	if len(r.Entry.TraceID) > maxTraceIDLength {
		return fmt.Errorf("trace ID too long (max %d characters)", maxTraceIDLength)
	}

	if len(r.Entry.Extra) > maxExtraEntries {
		return fmt.Errorf("too many extra context entries (max %d)", maxExtraEntries)
	}
	for k, v := range r.Entry.Extra {
		if len(k) > maxExtraKeyLength {
			return fmt.Errorf("extra context key too long: %q", k)
		}
		if len(v) > maxExtraValueLength {
			return fmt.Errorf("extra context value too long for key: %q", k)
		}
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

	handle, err := s.dbManager.GetAppDB(req.CollectorName, req.AppName)
	if err != nil {
		return 0, err
	}

	// Marshal extra context
	contextJSON := "{}"
	if len(req.Entry.Extra) > 0 {
		if b, err := json.Marshal(req.Entry.Extra); err == nil {
			contextJSON = string(b)
		}
	}

	var logID int64
	var logGroupID int64
	err = handle.Write(ctx, func(tx db.DBTX) error {
		q := db.New(tx)

		// Get or create log group for this message and level
		logGroup, err := q.GetOrCreateLogGroup(ctx, db.GetOrCreateLogGroupParams{
			Message: req.Entry.Message,
			Level:   string(req.Entry.Level),
		})
		if err != nil {
			return err
		}
		logGroupID = logGroup.ID

		// Store log entry as Unix timestamp
		ts := req.Entry.Timestamp.Unix()
		log, err := q.InsertLog(ctx, db.InsertLogParams{
			TraceID:   sql.NullString{String: req.Entry.TraceID, Valid: true},
			Level:     string(req.Entry.Level),
			Message:   req.Entry.Message,
			Context:   sql.NullString{String: contextJSON, Valid: true},
			GroupID:   sql.NullInt64{Int64: logGroup.ID, Valid: true},
			CreatedAt: ts,
		})
		if err != nil {
			return err
		}
		logID = log.ID

		// Increment the log group counter
		return q.IncrementLogGroupCounter(ctx, db.IncrementLogGroupCounterParams{
			ID:        logGroup.ID,
			LastLogAt: ts,
		})
	})
	if err != nil {
		return 0, err
	}

	// Send notification for errors
	if isError && s.notifyChan != nil {
		s.sendErrorNotification(req, logID, logGroupID)
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
	handle, err := s.dbManager.GetAppDB(req.CollectorName, req.AppName)
	if err != nil {
		return err
	}

	if err := handle.Write(ctx, func(tx db.DBTX) error {
		_, err := db.New(tx).InsertLifecycleEvent(ctx, db.InsertLifecycleEventParams{
			Kind:      string(req.Kind),
			RawLine:   req.RawLine,
			CreatedAt: req.Timestamp.Unix(),
		})
		return err
	}); err != nil {
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
	handle, err := s.dbManager.GetAppDB(req.CollectorName, req.AppName)
	if err != nil {
		return err
	}

	return handle.Write(ctx, func(tx db.DBTX) error {
		q := db.New(tx)

		current, err := q.GetOpenHealthPeriod(ctx)
		switch {
		case err == nil:
			if current.Status == string(req.Status) {
				return nil
			}
			if err := q.CloseHealthPeriod(ctx, db.CloseHealthPeriodParams{
				EndedAt: sql.NullInt64{Int64: req.Time.Unix(), Valid: true},
				ID:      current.ID,
			}); err != nil {
				return err
			}
		case errors.Is(err, sql.ErrNoRows):
		default:
			return err
		}

		return q.SaveHealthPeriod(ctx, db.SaveHealthPeriodParams{
			Status:    string(req.Status),
			StartedAt: req.Time.Unix(),
			EndedAt:   sql.NullInt64{},
		})
	})
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
