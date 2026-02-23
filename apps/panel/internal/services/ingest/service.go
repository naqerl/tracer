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

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/apps/panel/internal/services/notify"
	"github.com/ai-shift/tracer/libs/types"
)

var (
	ErrInvalidCollector = errors.New("invalid collector name")
	ErrInvalidApp       = errors.New("invalid app name")
	ErrInvalidEntry     = errors.New("invalid log entry")
	ErrInvalidStats     = errors.New("invalid stats data")
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

// Service manages ingest operations using dbmanager for all DB access.
type Service struct {
	dbManager  DBManager
	notifyChan notify.NotificationChan
	panelURL   string
}

// New creates a new ingest service.
func New(dbManager DBManager, notifyChan notify.NotificationChan, panelURL string) *Service {
	return &Service{
		dbManager:  dbManager,
		notifyChan: notifyChan,
		panelURL:   panelURL,
	}
}

// Request structs for service methods

type RegisterCollectorRequest struct {
	CollectorName string
	Apps          []string
}

func (r RegisterCollectorRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if len(r.Apps) == 0 {
		return fmt.Errorf("at least one app is required")
	}
	return nil
}

type InsertStatsRequest struct {
	CollectorName string
	Stats         SystemStats
}

func (r InsertStatsRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.Stats.CreatedAt.IsZero() {
		return fmt.Errorf("stats timestamp is required")
	}
	return nil
}

type StoreLogRequest struct {
	CollectorName string
	AppName       string
	Entry         types.LogEntry
	SampleRate    float64
}

func (r StoreLogRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	return nil
}

type StoreStartupRequest struct {
	CollectorName string
	AppName       string
	Entry         types.LogEntry
}

func (r StoreStartupRequest) Validate() error {
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	return nil
}

// ListCollectors returns all collector names.
func (s *Service) ListCollectors() ([]CollectorName, error) {
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
func (s *Service) InsertSystemStats(ctx context.Context, req InsertStatsRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}

	sqlDB, err := s.dbManager.CreateCollectorDB(req.CollectorName)
	if err != nil {
		return err
	}
	queries := db.New(sqlDB)
	return queries.InsertSystemStats(ctx, db.InsertSystemStatsParams{
		CollectorName:   req.Stats.CollectorName,
		CpuUsagePercent: req.Stats.CpuUsagePercent,
		MemTotalMb:      req.Stats.MemTotalMb,
		MemUsedMb:       req.Stats.MemUsedMb,
		MemAvailableMb:  req.Stats.MemAvailableMb,
		DiskTotalGb:     req.Stats.DiskTotalGb,
		DiskUsedGb:      req.Stats.DiskUsedGb,
		DiskAvailGb:     req.Stats.DiskAvailGb,
		CreatedAt:       req.Stats.CreatedAt,
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
	collectors, err := s.ListCollectors()
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

// RegisterCollector creates all necessary databases for a collector and its apps.
// This should be called when a collector first connects/register.
func (s *Service) RegisterCollector(ctx context.Context, req RegisterCollectorRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}

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

// GetAppDB returns the database connection for the given collector/app.
// Assumes the database already exists (created during registration).
func (s *Service) GetAppDB(collectorName, appName string) (*sql.DB, error) {
	return s.dbManager.CreateAppDB(collectorName, appName)
}

// StoreLog stores a log entry for a specific app.
// It handles log group creation and insertion of the log entry.
// Returns the log ID for potential notifications.
// Includes sampling logic and notification sending for errors.
func (s *Service) StoreLog(ctx context.Context, req StoreLogRequest) (int64, error) {
	if err := req.Validate(); err != nil {
		return 0, err
	}

	// Check if this is an error level log
	isError := req.Entry.Level == types.LevelError

	// Sampling logic: always persist errors, sample others
	if !isError && rand.Float64() >= req.SampleRate {
		return 0, nil // Skipped by sampling
	}

	sqlDB, err := s.dbManager.CreateAppDB(req.CollectorName, req.AppName)
	if err != nil {
		return 0, err
	}
	queries := db.New(sqlDB)

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
	if ts.IsZero() {
		ts = time.Now()
	}
	ts = ts.UTC()

	log, err := queries.InsertLog(ctx, db.InsertLogParams{
		TraceID:   req.Entry.TraceID,
		Level:     string(req.Entry.Level),
		Message:   req.Entry.Message,
		Source:    req.Entry.Source,
		Context:   sql.NullString{String: contextJSON, Valid: true},
		Raw:       req.Entry.Raw,
		GroupID:   sql.NullInt64{Int64: logGroup.ID, Valid: true},
		CreatedAt: sql.NullTime{Time: ts, Valid: true},
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

// StoreStartup stores a startup log entry for a specific app.
// Similar to StoreLog but uses "STARTUP" as the level.
// Sends startup notification.
func (s *Service) StoreStartup(ctx context.Context, req StoreStartupRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}

	sqlDB, err := s.dbManager.CreateAppDB(req.CollectorName, req.AppName)
	if err != nil {
		return err
	}
	queries := db.New(sqlDB)

	// Marshal extra context
	contextJSON := "{}"
	if len(req.Entry.Extra) > 0 {
		if b, err := json.Marshal(req.Entry.Extra); err == nil {
			contextJSON = string(b)
		}
	}

	// Use "STARTUP" as the level for startup events
	level := "STARTUP"
	if req.Entry.Level != "" {
		level = string(req.Entry.Level)
	}

	// Get or create log group for this message and level
	logGroup, err := queries.GetOrCreateLogGroup(ctx, db.GetOrCreateLogGroupParams{
		Message: req.Entry.Message,
		Level:   level,
	})
	if err != nil {
		return err
	}

	// Store startup entry
	ts := req.Entry.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	ts = ts.UTC()

	_, err = queries.InsertLog(ctx, db.InsertLogParams{
		TraceID:   req.Entry.TraceID,
		Level:     level,
		Message:   req.Entry.Message,
		Source:    req.Entry.Source,
		Context:   sql.NullString{String: contextJSON, Valid: true},
		Raw:       req.Entry.Raw,
		GroupID:   sql.NullInt64{Int64: logGroup.ID, Valid: true},
		CreatedAt: sql.NullTime{Time: ts, Valid: true},
	})
	if err != nil {
		return err
	}

	// Send startup notification
	if s.notifyChan != nil {
		s.sendStartupNotification(req)
	}

	return nil
}

// sendErrorNotification sends a notification for error logs
func (s *Service) sendErrorNotification(req StoreLogRequest, logID, groupID int64) {
	// Build the alert message with markdown link
	alertMessage := fmt.Sprintf("```%s```", req.Entry.Message)
	if s.panelURL != "" {
		// Include log ID and group ID in URL
		logsURL := fmt.Sprintf("%s/panel/collectors/%s/apps/%s/logs/%d",
			s.panelURL,
			req.CollectorName,
			req.AppName,
			logID)
		alertMessage += fmt.Sprintf("\n\n[view log](%s)", logsURL)
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

// sendStartupNotification sends a notification for app startup
func (s *Service) sendStartupNotification(req StoreStartupRequest) {
	// Build the startup message with markdown link
	startupMessage := req.Entry.Message
	if s.panelURL != "" {
		logsURL := fmt.Sprintf("%s/panel/collectors/%s/apps/%s/logs",
			s.panelURL,
			req.CollectorName,
			req.AppName)
		startupMessage += fmt.Sprintf("\n\n[view logs](%s)", logsURL)
	}

	select {
	case s.notifyChan <- notify.Notification{
		Type:          notify.TypeStartup,
		CollectorName: req.CollectorName,
		AppName:       req.AppName,
		Message:       startupMessage,
	}:
	default:
		slog.Warn("Notification channel full, dropping startup notification")
	}
}
