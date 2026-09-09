package panel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/user"
	sharedtypes "tracer/libs/types"
)

var (
	ErrCollectorNotFound = errors.New("collector not found")
	ErrAppNotFound       = errors.New("app not found")
	ErrLogNotFound       = errors.New("log not found")
	ErrAccessDenied      = errors.New("access denied")
)

const (
	maxSinceAge         = 30 * 24 * time.Hour
	sinceValidationSkew = time.Second
)

func New(dbManager *dbmanager.Manager, ingestSvc *ingest.Service, tracker *ingest.Tracker, userSvc *user.Service, pusher ConfigPusher, guesser FormatGuesser) *Service {
	return &Service{
		dbManager: dbManager,
		ingestSvc: ingestSvc,
		userSvc:   userSvc,
		tracker:   tracker,
		pusher:    pusher,
		guesser:   guesser,
	}
}

type Service struct {
	dbManager *dbmanager.Manager
	ingestSvc *ingest.Service
	userSvc   *user.Service
	tracker   *ingest.Tracker
	pusher    ConfigPusher
	guesser   FormatGuesser
}

// ConfigPusher delivers configuration to collectors that are currently
// connected. The implementation (a live WebSocket registry) is supplied by the
// transport layer; it is optional so non-serving contexts (tests) can omit it.
type ConfigPusher interface {
	PushCollectorConfig(collector, configJSON string)
}

// FormatGuesser sends a format guess request to a collector over WebSocket
// and waits for the result. The transport layer implements this.
type FormatGuesser interface {
	GuessFormat(ctx context.Context, collector string, req sharedtypes.FormatRequestEvent) (sharedtypes.FormatResultEvent, error)
}

// Collector provides a complete view of a collector for display,
// with data pre-filtered according to user permissions.
type Collector struct {
	Name        string
	RemoteAddr  string
	IsConnected bool
	ConnectedAt time.Time
	LastSeenAt  time.Time
	Stats       *CollectorStats
}

// CollectorStats holds the collector stats required by the UI.
type CollectorStats struct {
	CPUUsagePercent    float64
	MemTotalMB         int64
	MemUsedMB          int64
	DiskTotalGB        int64
	DiskUsedGB         int64
	TimestampFormatted string
	Staleness          string // "fresh", "warning", "stale", "old"
}

// ListCollectors returns collectors the user can access.
// Tracker data only enriches connection metadata.
func (s *Service) ListCollectors(ctx context.Context, userID int64) ([]Collector, error) {
	collectors, err := s.listAllCollectors(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not list all collectors: %w", err)
	}

	access, err := s.userSvc.FindAccessPolicy(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("could not load apps access for user %d: %w", userID, err)
	}

	var i int
	for _, collector := range collectors {
		if !access.AllowedTo(collector.Name, "") {
			continue
		}
		collectors[i] = collector
		i++
	}
	return collectors[:i], nil
}

type GetCollectorRequest struct {
	UserID        int64
	CollectorName string
}

func (r GetCollectorRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	return nil
}

func (s *Service) GetCollector(ctx context.Context, req GetCollectorRequest) (Collector, error) {
	exists, err := s.CollectorExists(req.CollectorName)
	if err != nil {
		return Collector{}, fmt.Errorf("could not ensure collector existence: %w", err)
	}
	if !exists {
		return Collector{}, ErrCollectorNotFound
	}

	collector, err := s.getCollector(ctx, req.CollectorName)
	if err != nil {
		return Collector{}, fmt.Errorf("could not load collector data for %s: %w", req.CollectorName, err)
	}

	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return Collector{}, fmt.Errorf("could not load access for user %d: %w", req.UserID, err)
	}
	if !access.AllowedTo(req.CollectorName, "") {
		return Collector{}, ErrAccessDenied
	}

	return collector, nil
}

// FetchCollectorConfig returns the panel-driven configuration for a
// collector. An empty string means no configuration has been stored yet.
func (s *Service) FetchCollectorConfig(ctx context.Context, collectorName string) (string, error) {
	if err := domain.ValidateCollectorName(collectorName); err != nil {
		return "", fmt.Errorf("invalid collector name: %w", err)
	}

	handle, err := s.dbManager.GetCollectorDB(collectorName)
	if err != nil {
		if errors.Is(err, dbmanager.ErrDBNotFound) {
			return "", nil
		}
		return "", fmt.Errorf("could not open collector database: %w", err)
	}

	cfg, err := db.New(handle.Read()).GetCollectorConfig(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("could not read collector config: %w", err)
	}
	return cfg, nil
}

// SaveCollectorConfig persists the panel-driven configuration for a
// collector. configJSON is the JSON-encoded CollectorConfig. It upserts
// the single config row and pushes the update to any connected collectors.
func (s *Service) SaveCollectorConfig(ctx context.Context, collectorName, configJSON string) error {
	if err := domain.ValidateCollectorName(collectorName); err != nil {
		return fmt.Errorf("invalid collector name: %w", err)
	}
	if strings.TrimSpace(configJSON) == "" {
		return fmt.Errorf("config json is required")
	}

	var cfg sharedtypes.CollectorConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return fmt.Errorf("invalid collector config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid collector config: %w", err)
	}
	if cfg.Stats == nil {
		def := sharedtypes.DefaultStatsConfig()
		cfg.Stats = &def
		updated, err := json.Marshal(&cfg)
		if err != nil {
			return fmt.Errorf("could not marshal collector config: %w", err)
		}
		configJSON = string(updated)
	}

	handle, err := s.dbManager.GetCollectorDB(collectorName)
	if err != nil {
		if errors.Is(err, dbmanager.ErrDBNotFound) {
			handle, err = s.dbManager.CreateCollectorDB(collectorName)
		}
		if err != nil {
			return fmt.Errorf("could not open collector database: %w", err)
		}
	}

	if err := handle.Write(ctx, func(tx db.DBTX) error {
		return db.New(tx).UpsertCollectorConfig(ctx, db.UpsertCollectorConfigParams{
			ConfigJson: configJSON,
			UpdatedAt:  time.Now().Unix(),
		})
	}); err != nil {
		return fmt.Errorf("could not store collector config: %w", err)
	}

	if s.pusher != nil {
		s.pusher.PushCollectorConfig(collectorName, configJSON)
	}
	return nil
}

// DeleteCollectorApp removes an app from the collector's stored config and
// deletes its retained database so it stops appearing in dashboards.
func (s *Service) DeleteCollectorApp(ctx context.Context, collectorName, appName string) error {
	if err := domain.ValidateCollectorName(collectorName); err != nil {
		return fmt.Errorf("invalid collector name: %w", err)
	}
	if err := domain.ValidateAppName(appName); err != nil {
		return fmt.Errorf("invalid app name: %w", err)
	}

	existing, err := s.FetchCollectorConfig(ctx, collectorName)
	if err != nil {
		return fmt.Errorf("could not load config: %w", err)
	}

	var cfg sharedtypes.CollectorConfig
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &cfg); err != nil {
			return fmt.Errorf("invalid stored config: %w", err)
		}
	}
	cfg.RemoveApp(appName)
	updated, err := json.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}

	if err := s.SaveCollectorConfig(ctx, collectorName, string(updated)); err != nil {
		return fmt.Errorf("could not save config: %w", err)
	}

	if err := s.dbManager.DeleteAppDB(collectorName, appName); err != nil {
		return fmt.Errorf("could not delete app database: %w", err)
	}
	return nil
}

// GuessFormatAndSave sends a format guess request to the collector via
// WebSocket, builds an app config from the result, saves it, and pushes
// the updated config to the collector. Returns an error on failure.
func (s *Service) GuessFormatAndSave(ctx context.Context, collectorName, appName string) error {
	if s.guesser == nil {
		return fmt.Errorf("format guessing not available")
	}

	requestID := uuid.NewString()
	result, err := s.guesser.GuessFormat(ctx, collectorName, sharedtypes.FormatRequestEvent{
		Type:      sharedtypes.EventTypeFormatRequest,
		RequestID: requestID,
		App:       appName,
		Service:   appName,
	})
	if err != nil {
		return fmt.Errorf("could not detect format: %w", err)
	}
	if result.Error != "" {
		return fmt.Errorf("format detection failed: %s", result.Error)
	}

	existing, err := s.FetchCollectorConfig(ctx, collectorName)
	if err != nil {
		return fmt.Errorf("could not load config: %w", err)
	}

	var cfg sharedtypes.CollectorConfig
	if existing != "" {
		if jErr := json.Unmarshal([]byte(existing), &cfg); jErr != nil {
			return fmt.Errorf("invalid stored config: %w", jErr)
		}
	}

	if err := cfg.AddApp(appName, *result.App.Source, sharedtypes.ParserSpec{
		Type:   result.Parser.Type,
		Params: result.Parser.Params,
	}); err != nil {
		return fmt.Errorf("could not add app: %w", err)
	}

	updated, err := json.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}

	if err := s.SaveCollectorConfig(ctx, collectorName, string(updated)); err != nil {
		return fmt.Errorf("could not save config: %w", err)
	}

	return nil
}

// PreviewFormat asks the collector to detect the log format for a service
// without saving anything. It returns the raw result so the caller can
// distinguish a not-found service, a guess conflict, and a successful guess.
// favour carries a user-chosen resolution for a prior conflict.
func (s *Service) PreviewFormat(ctx context.Context, collectorName, serviceName string, favour sharedtypes.ParserSpec) (sharedtypes.FormatResultEvent, error) {
	if s.guesser == nil {
		return sharedtypes.FormatResultEvent{}, fmt.Errorf("format guessing not available")
	}

	requestID := uuid.NewString()
	result, err := s.guesser.GuessFormat(ctx, collectorName, sharedtypes.FormatRequestEvent{
		Type:      sharedtypes.EventTypeFormatRequest,
		RequestID: requestID,
		App:       serviceName,
		Service:   serviceName,
		Favour:    favour,
	})
	if err != nil {
		return result, fmt.Errorf("could not detect format: %w", err)
	}
	return result, nil
}

// ErrCannotModifyCollector indicates the user lacks permission to modify a collector.
var ErrCannotModifyCollector = errors.New("cannot modify collector")

// CanModifyCollector reports whether the user is admin or has access to the collector.
func (s *Service) CanModifyCollector(ctx context.Context, userID int64, collectorName string) error {
	access, err := s.userSvc.FindAccessPolicy(ctx, userID)
	if err != nil {
		return fmt.Errorf("could not load access for user %d: %w", userID, err)
	}
	if access.AllowedTo(collectorName, "") {
		return nil
	}
	return ErrCannotModifyCollector
}

// CollectorExists reports whether a collector with the given name exists.
func (s *Service) CollectorExists(name string) (bool, error) {
	collectors, err := s.dbManager.ListCollectors()
	if err != nil {
		return false, err
	}
	for _, collector := range collectors {
		if collector == name {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) listAllCollectors(ctx context.Context) ([]Collector, error) {
	collectorNames, err := s.dbManager.ListCollectors()
	if err != nil {
		return nil, fmt.Errorf("list collectors: %w", err)
	}

	views := make([]Collector, 0, len(collectorNames))
	for _, collectorName := range collectorNames {
		view, err := s.getCollector(ctx, collectorName)
		if err != nil {
			slog.Error("Failed to get collector", "collector", collectorName, "error", err)
			continue
		}

		views = append(views, view)
	}

	return views, nil
}

func (s *Service) getCollector(ctx context.Context, collectorName string) (Collector, error) {
	view := Collector{
		Name: collectorName,
	}

	if trackerInfo, exists := s.tracker.Get(view.Name); exists {
		view.RemoteAddr = trackerInfo.RemoteAddr
		view.IsConnected = trackerInfo.IsConnected
		view.ConnectedAt = trackerInfo.ConnectedAt
		view.LastSeenAt = trackerInfo.LastSeenAt
	}

	stats, err := s.ingestSvc.GetLatestStats(ctx, collectorName)
	if err == nil {
		view.Stats = formatCollectorStats(stats, time.Now())
		return view, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return view, nil
	}

	return view, err
}

type ListCollectorAppsRequest struct {
	UserID        int64
	CollectorName string
}

func (r ListCollectorAppsRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	return nil
}

// CollectorApp is an app in the apps grid with its uptime and error-count
// stats.
type CollectorApp struct {
	Name          string
	UptimePercent float64
	UptimePeriods int
	ErrorCount    int64
	HasLogs       bool
}

// ListCollectorAppsWithStatsRequest requests the accessible apps of a
// collector with stats, within the given time window.
type ListCollectorAppsWithStatsRequest struct {
	UserID        int64
	CollectorName string
	Since         time.Time
}

func (r ListCollectorAppsWithStatsRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	return nil
}

// ListCollectorApps returns the names of the collector's apps the user may
// see.
func (s *Service) ListCollectorApps(ctx context.Context, req ListCollectorAppsRequest) ([]string, error) {
	return s.listAccessibleApps(ctx, req.UserID, req.CollectorName)
}

// ListCollectorAppsWithStats returns the accessible apps with their uptime
// and error-count stats.
func (s *Service) ListCollectorAppsWithStats(ctx context.Context, req ListCollectorAppsWithStatsRequest) ([]CollectorApp, error) {
	apps, err := s.listAccessibleApps(ctx, req.UserID, req.CollectorName)
	if err != nil {
		return nil, err
	}

	type indexedApp struct {
		idx int
		app CollectorApp
	}

	results := make(chan indexedApp, len(apps))
	var wg sync.WaitGroup
	for i, app := range apps {
		wg.Go(func() {
			results <- indexedApp{idx: i, app: s.appStats(ctx, req.CollectorName, app, req.Since)}
		})
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	stats := make([]CollectorApp, len(apps))
	for r := range results {
		stats[r.idx] = r.app
	}
	return stats, nil
}

// listAccessibleApps is the shared access-check + name-listing core of
// ListCollectorApps and ListCollectorAppsWithStats.
func (s *Service) listAccessibleApps(ctx context.Context, userID int64, collectorName string) ([]string, error) {
	exists, err := s.CollectorExists(collectorName)
	if err != nil {
		return nil, fmt.Errorf("could not ensure collector existence: %w", err)
	}
	if !exists {
		return nil, ErrCollectorNotFound
	}

	access, err := s.userSvc.FindAccessPolicy(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("could not load access for user %d: %w", userID, err)
	}
	if !access.AllowedTo(collectorName, "") {
		return nil, ErrAccessDenied
	}

	apps, err := s.ingestSvc.ListApps(collectorName)
	if err != nil {
		return nil, fmt.Errorf("could not list apps for collector %s: %w", collectorName, err)
	}
	return filterAccessibleApps(access, collectorName, apps), nil
}

// filterAccessibleApps returns the subset of the collector's apps the user may
// see. Admins (no access policy rows) see every app.
func filterAccessibleApps(access user.AccessPolicy, collectorName string, apps []string) []string {
	if access.Apps == nil {
		return apps
	}
	filtered := make([]string, 0, len(apps))
	for _, app := range apps {
		if access.AllowedTo(collectorName, app) {
			filtered = append(filtered, app)
		}
	}
	return filtered
}

// appStats reads the uptime percentage and error count for a single app.
func (s *Service) appStats(ctx context.Context, collectorName, appName string, since time.Time) CollectorApp {
	app := CollectorApp{Name: appName}

	queries, err := s.queriesForApp(collectorName, appName)
	if errors.Is(err, ErrAppNotFound) {
		return app
	}
	if err != nil {
		slog.Error("could not open app db for grid stats", "collector", collectorName, "app", appName, "error", err)
		return app
	}

	health, err := s.readAppHealth(ctx, queries, since)
	if err != nil {
		slog.Error("could not read app health for grid stats", "collector", collectorName, "app", appName, "error", err)
	} else {
		app.UptimePercent = health.UptimePercent
		app.UptimePeriods = health.PeriodCount
	}

	counts, err := queries.GetPercentiles(ctx, since.Unix())
	if err != nil {
		slog.Error("could not read app stats for grid", "collector", collectorName, "app", appName, "error", err)
	} else {
		app.ErrorCount = counts.Errors
		hasLogs, err := queries.HasLog(ctx)
		if err != nil {
			slog.Error("could not read app log presence for grid", "collector", collectorName, "app", appName, "error", err)
		} else {
			app.HasLogs = hasLogs
		}
	}
	return app
}

type AppHealth struct {
	CurrentStatus sharedtypes.HealthStatus
	PeriodCount   int
	UptimePercent float64
	Buckets       []HealthBucket
}

type HealthBucket struct {
	Start        time.Time
	End          time.Time
	UpDuration   time.Duration
	DownDuration time.Duration
	Checks       int
}

type GetAppHealthRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	Since         time.Time
}

func (r GetAppHealthRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	return nil
}

func (s *Service) GetAppHealth(ctx context.Context, req GetAppHealthRequest) (AppHealth, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return AppHealth{}, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return AppHealth{}, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return AppHealth{}, err
	}

	return s.readAppHealthWithBars(ctx, queries, req.Since)
}

type GetStatsHistoryRequest struct {
	UserID        int64
	CollectorName string
	Since         time.Time
}

func (r GetStatsHistoryRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	return nil
}

func sinceWithinRange(since time.Time) bool {
	return time.Since(since) <= maxSinceAge+sinceValidationSkew
}

func (s *Service) GetStatsHistory(ctx context.Context, req GetStatsHistoryRequest) (ingest.StatsHistoryResult, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return ingest.StatsHistoryResult{}, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, "") {
		return ingest.StatsHistoryResult{}, ErrAccessDenied
	}

	return s.ingestSvc.GetStatsHistory(ctx, req.CollectorName, req.Since)
}

type GetAppStatsRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	Since         time.Time
}

type AppStats struct {
	Total    int64
	Errors   int64
	Warnings int64
	Infos    int64
	HasLogs  bool
}

func (r GetAppStatsRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	return nil
}

func (s *Service) GetAppStats(ctx context.Context, req GetAppStatsRequest) (AppStats, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return AppStats{}, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return AppStats{}, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return AppStats{}, err
	}

	counts, err := queries.GetPercentiles(ctx, req.Since.Unix())
	if err != nil {
		return AppStats{}, fmt.Errorf("could not read app stats: %w", err)
	}
	hasLogs, err := queries.HasLog(ctx)
	if err != nil {
		return AppStats{}, fmt.Errorf("could not read app log presence: %w", err)
	}

	return AppStats{
		Total:    counts.Total,
		Errors:   counts.Errors,
		Warnings: counts.Warnings,
		Infos:    counts.Infos,
		HasLogs:  hasLogs,
	}, nil
}

type GetLogGroupsRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	Level         string
	Since         time.Time
	Offset        int
	Limit         int
}

type LogGroup struct {
	ID        int64
	Message   string
	Level     string
	Count     int64
	LastLogAt time.Time
	CreatedAt time.Time
}

func (r GetLogGroupsRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.Offset < 0 {
		return fmt.Errorf("offset must be non-negative")
	}
	if r.Limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}
	return nil
}

func (s *Service) GetLogGroups(ctx context.Context, req GetLogGroupsRequest) ([]LogGroup, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return nil, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return nil, err
	}

	level := req.Level
	if level == "all" {
		level = "%"
	}

	rows, err := queries.ListLogGroups(ctx, db.ListLogGroupsParams{
		CreatedAt: req.Since.Unix(),
		Level:     level,
		Limit:     int64(req.Limit),
		Offset:    int64(req.Offset),
	})
	if err != nil {
		return nil, err
	}

	groups := make([]LogGroup, 0, len(rows))
	for _, row := range rows {
		groups = append(groups, LogGroup{
			ID:        row.ID,
			Message:   row.Message,
			Level:     row.Level,
			Count:     row.Count,
			LastLogAt: time.Unix(row.LastLogAt, 0),
			CreatedAt: time.Unix(row.CreatedAt, 0),
		})
	}

	return groups, nil
}

type GetLogGroupRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	GroupID       int64
}

func (r GetLogGroupRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.GroupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	return nil
}

func (s *Service) GetLogGroup(ctx context.Context, req GetLogGroupRequest) (LogGroup, error) {
	var group LogGroup

	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return group, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return group, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return group, err
	}

	row, err := queries.GetLogGroupByID(ctx, sql.NullInt64{Int64: req.GroupID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return group, ErrLogNotFound
	}
	if err != nil {
		return group, err
	}

	return LogGroup{
		ID:        req.GroupID,
		Message:   row.Message,
		Level:     row.Level,
		Count:     row.LogCount,
		CreatedAt: time.Unix(row.CreatedAt, 0),
		LastLogAt: time.Unix(row.LastLogAt, 0),
	}, nil
}

// GetLogGroupSummaryRequest asks for a log group's summary plus the count of
// rows created after Since.
type GetLogGroupSummaryRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	GroupID       int64
	Since         int64
}

func (r GetLogGroupSummaryRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.GroupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	if r.Since < 0 {
		return fmt.Errorf("since must be non-negative")
	}
	return nil
}

// LogGroupSummary is a log group plus the count of rows created after Since.
type LogGroupSummary struct {
	LogGroup
	NewLogs int64
}

func (s *Service) GetLogGroupSummary(ctx context.Context, req GetLogGroupSummaryRequest) (LogGroupSummary, error) {
	var summary LogGroupSummary

	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return summary, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return summary, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return summary, err
	}

	row, err := queries.GetLogGroupByID(ctx, sql.NullInt64{Int64: req.GroupID, Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return summary, ErrLogNotFound
	}
	if err != nil {
		return summary, err
	}

	summary.LogGroup = LogGroup{
		ID:        req.GroupID,
		Message:   row.Message,
		Level:     row.Level,
		Count:     row.LogCount,
		CreatedAt: time.Unix(row.CreatedAt, 0),
		LastLogAt: time.Unix(row.LastLogAt, 0),
	}

	if req.Since > 0 {
		newLogs, err := queries.CountLogsByGroupIDSince(ctx, db.CountLogsByGroupIDSinceParams{
			GroupID:   sql.NullInt64{Int64: req.GroupID, Valid: true},
			CreatedAt: req.Since,
		})
		if err != nil {
			return summary, err
		}
		summary.NewLogs = newLogs
	}

	return summary, nil
}

type GroupLogEntry struct {
	ID        int64
	Level     string
	Context   sql.NullString
	CreatedAt time.Time
}

type ListGroupLogsRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	GroupID       int64
	Since         time.Time
	Offset        int
	Limit         int
}

func (r ListGroupLogsRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.GroupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	if r.Offset < 0 {
		return fmt.Errorf("offset must be non-negative")
	}
	if r.Limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}
	return nil
}

func (s *Service) ListGroupLogs(ctx context.Context, req ListGroupLogsRequest) ([]GroupLogEntry, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return nil, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return nil, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return nil, err
	}

	rows, err := queries.GetLogsByGroupID(ctx, db.GetLogsByGroupIDParams{
		GroupID:   sql.NullInt64{Int64: req.GroupID, Valid: true},
		CreatedAt: req.Since.Unix(),
		Limit:     int64(req.Limit),
		Offset:    int64(req.Offset),
	})
	if err != nil {
		return nil, err
	}

	entries := make([]GroupLogEntry, len(rows))
	for i, row := range rows {
		entries[i] = GroupLogEntry{
			ID:        row.ID,
			Level:     row.Level,
			Context:   row.Context,
			CreatedAt: time.Unix(row.CreatedAt, 0),
		}
	}

	return entries, nil
}

type GetLogByIDRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	ID            int64
}

func (r GetLogByIDRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.ID <= 0 {
		return fmt.Errorf("log ID must be positive")
	}
	return nil
}

func (s *Service) GetLogByID(ctx context.Context, req GetLogByIDRequest) (db.Log, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return db.Log{}, fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return db.Log{}, ErrAccessDenied
	}

	queries, err := s.queriesForApp(req.CollectorName, req.AppName)
	if err != nil {
		return db.Log{}, err
	}

	logEntry, err := queries.GetLogByID(ctx, req.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return db.Log{}, ErrLogNotFound
		}
		return db.Log{}, fmt.Errorf("get log: %w", err)
	}

	return logEntry, nil
}

type DeleteLogGroupRequest struct {
	UserID        int64
	CollectorName string
	AppName       string
	GroupID       int64
}

func (r DeleteLogGroupRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if r.CollectorName == "" {
		return fmt.Errorf("collector name is required")
	}
	if r.AppName == "" {
		return fmt.Errorf("app name is required")
	}
	if r.GroupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	return nil
}

func (s *Service) DeleteLogGroup(ctx context.Context, req DeleteLogGroupRequest) error {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return fmt.Errorf("could not list user apps: %w", err)
	}
	if !access.AllowedTo(req.CollectorName, req.AppName) {
		return ErrAccessDenied
	}

	handle, err := s.dbManager.GetAppDB(req.CollectorName, req.AppName)
	if err != nil {
		if err == dbmanager.ErrDBNotFound {
			return ErrAppNotFound
		}
		return err
	}

	return handle.Write(ctx, func(tx db.DBTX) error {
		rowsAffected, err := db.New(tx).DeleteLogGroup(ctx, req.GroupID)
		if err != nil {
			return fmt.Errorf("delete log group: %w", err)
		}
		if rowsAffected == 0 {
			return ErrLogNotFound
		}
		return nil
	})
}

func (s *Service) GetLogVolumeTimeSeries(ctx context.Context, collectorName, appName string, since time.Time, granularity string) ([]db.GetLogVolumeTimeSeriesRow, error) {
	queries, err := s.queriesForApp(collectorName, appName)
	if err != nil {
		return nil, err
	}

	return queries.GetLogVolumeTimeSeries(ctx, db.GetLogVolumeTimeSeriesParams{
		Since:       since.Unix(),
		Granularity: granularity,
	})
}

func (s *Service) queriesForApp(collectorName, appName string) (*db.Queries, error) {
	handle, err := s.dbManager.GetAppDB(collectorName, appName)
	if err != nil {
		if err == dbmanager.ErrDBNotFound {
			return nil, ErrAppNotFound
		}
		return nil, err
	}
	return db.New(handle.Read()), nil
}

// formatCollectorStats formats a system stat for display. now is the
// reference time used for staleness; production passes time.Now(), tests pass
// a fixed value so staleness buckets are deterministic regardless of the wall
// clock (a stat 45 min old must not flip from "stale" to "old" at midnight).
func formatCollectorStats(stats ingest.SystemStat, now time.Time) *CollectorStats {
	createdAt := time.Unix(stats.Timestamp, 0)
	age := now.Sub(createdAt)

	nowY, nowM, nowD := now.Date()
	statY, statM, statD := createdAt.Date()
	sameDate := nowY == statY && nowM == statM && nowD == statD

	var staleness string
	var formatted string

	if !sameDate {
		staleness = "old"
		formatted = createdAt.Format("2006-01-02 15:04:05")
	} else if age > 30*time.Minute {
		staleness = "stale"
		formatted = createdAt.Format("15:04:05")
	} else if age > 5*time.Minute {
		staleness = "warning"
		formatted = createdAt.Format("15:04:05")
	} else {
		staleness = "fresh"
		formatted = createdAt.Format("15:04:05")
	}

	return &CollectorStats{
		CPUUsagePercent:    stats.CPUUsagePercent,
		MemTotalMB:         stats.MemTotalMb,
		MemUsedMB:          stats.MemUsedMb,
		DiskTotalGB:        stats.DiskTotalGb,
		DiskUsedGB:         stats.DiskUsedGb,
		TimestampFormatted: formatted,
		Staleness:          staleness,
	}
}

// readAppHealth reads uptime stats for an app in the given window.
func (s *Service) readAppHealth(ctx context.Context, queries *db.Queries, since time.Time) (AppHealth, error) {
	health, _, err := s.readHealthAndPeriods(ctx, queries, since)
	return health, err
}

// readAppHealthWithBars is readAppHealth plus per-period bucket bars for the
// uptime visualization.
func (s *Service) readAppHealthWithBars(ctx context.Context, queries *db.Queries, since time.Time) (AppHealth, error) {
	health, periods, err := s.readHealthAndPeriods(ctx, queries, since)
	if err != nil {
		return health, err
	}

	now := time.Now()
	var checkCounts []db.ListHealthCheckCountsBySlotRow
	if now.After(since) {
		bucketSize := healthBucketSize(now.Sub(since), healthWindowBucketCount(len(periods)))
		checkCounts, err = queries.ListHealthCheckCountsBySlot(ctx, db.ListHealthCheckCountsBySlotParams{
			At:   since.Unix(),
			At_2: int64(bucketSize / time.Second),
			At_3: since.Unix(),
			At_4: now.Unix(),
		})
		if err != nil {
			return health, err
		}
	}

	health.Buckets = buildHealthBuckets(periods, checkCounts, since, now)
	return health, nil
}

// readHealthAndPeriods computes uptime stats plus the health periods they
// were derived from.
func (s *Service) readHealthAndPeriods(ctx context.Context, queries *db.Queries, since time.Time) (AppHealth, []db.AppHealthPeriod, error) {
	health := AppHealth{}

	latest, err := queries.GetLatestHealthPeriod(ctx)
	switch {
	case err == nil:
		health.CurrentStatus = sharedtypes.HealthStatus(latest.Status)
	case errors.Is(err, sql.ErrNoRows):
	default:
		return health, nil, err
	}

	now := time.Now()
	periods, err := queries.ListHealthPeriodsOverlappingRange(ctx, db.ListHealthPeriodsOverlappingRangeParams{
		StartedAt: now.Unix(),
		EndedAt:   sql.NullInt64{Int64: since.Unix(), Valid: true},
	})
	if err != nil {
		return health, nil, err
	}

	health.PeriodCount = len(periods)
	upDuration := time.Duration(0)
	downDuration := time.Duration(0)
	for _, period := range periods {
		overlap := overlapDuration(period, since, now)
		if overlap <= 0 {
			continue
		}
		switch sharedtypes.HealthStatus(period.Status) {
		case sharedtypes.HealthStatusUp:
			upDuration += overlap
		case sharedtypes.HealthStatusDown:
			downDuration += overlap
		}
	}

	totalDuration := upDuration + downDuration
	if totalDuration > 0 {
		health.UptimePercent = math.Round((float64(upDuration) / float64(totalDuration) * 10000)) / 100
	}

	return health, periods, nil
}

// healthWindowBucketCount returns the number of uptime buckets shown for a
// window; periods set a floor so sparse data still fills the visualization.
func healthWindowBucketCount(periodCount int) int {
	if periodCount < 37 {
		return 37
	}
	return periodCount
}

// healthBucketSize divides the window into whole-second buckets.
func healthBucketSize(window time.Duration, bucketCount int) time.Duration {
	size := int64(window/time.Second) / int64(bucketCount)
	if size <= 0 {
		size = 1
	}
	return time.Duration(size) * time.Second
}

func buildHealthBuckets(periods []db.AppHealthPeriod, checkCounts []db.ListHealthCheckCountsBySlotRow, since, now time.Time) []HealthBucket {
	if now.Before(since) {
		now = since
	}

	bucketCount := healthWindowBucketCount(len(periods))

	window := now.Sub(since)
	if window <= 0 {
		return []HealthBucket{{Start: since, End: now}}
	}

	bucketSize := healthBucketSize(window, bucketCount)

	buckets := make([]HealthBucket, 0, bucketCount)
	current := since
	for i := 0; i < bucketCount; i++ {
		next := current.Add(bucketSize)
		if i == bucketCount-1 || next.After(now) {
			next = now
		}

		bucket := HealthBucket{
			Start: current,
			End:   next,
		}
		for _, period := range periods {
			overlap := overlapDuration(period, current, next)
			if overlap <= 0 {
				continue
			}
			switch sharedtypes.HealthStatus(period.Status) {
			case sharedtypes.HealthStatusUp:
				bucket.UpDuration += overlap
			case sharedtypes.HealthStatusDown:
				bucket.DownDuration += overlap
			}
		}
		buckets = append(buckets, bucket)
		current = next
	}

	lastSlot := int64(bucketCount - 1)
	for _, row := range checkCounts {
		slot := row.Slot
		if slot > lastSlot {
			slot = lastSlot
		}
		buckets[slot].Checks += int(row.Cnt)
	}

	return buckets
}

func overlapDuration(period db.AppHealthPeriod, start, end time.Time) time.Duration {
	periodStart := time.Unix(period.StartedAt, 0)
	periodEnd := end
	if period.EndedAt.Valid {
		periodEnd = time.Unix(period.EndedAt.Int64, 0)
	}
	if periodEnd.Before(periodStart) {
		periodEnd = periodStart
	}

	overlapStart := maxTime(start, periodStart)
	overlapEnd := minTime(end, periodEnd)
	if !overlapEnd.After(overlapStart) {
		return 0
	}
	return overlapEnd.Sub(overlapStart)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// PanelStatsRequest requests the panel-wide stats page data.
type PanelStatsRequest struct {
	UserID int64
	Since  time.Time
}

func (r PanelStatsRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	if !sinceWithinRange(r.Since) {
		return fmt.Errorf("since should be within last 30 days")
	}
	return nil
}

// PanelAppStats holds per-app stats for the panel stats page.
type PanelAppStats struct {
	Name       string
	LogCount   int64
	IsStray    bool
	Percentile PanelPercentiles
}

// PanelPercentiles holds log volume percentile data for an app.
type PanelPercentiles struct {
	P50 int64
	P75 int64
	P95 int64
	P99 int64
	Max int64
	Min int64
}

// PanelCollectorStats holds stats for one collector on the panel stats page.
type PanelCollectorStats struct {
	Name        string
	DiskUsedGB  int64
	DiskTotalGB int64
	Apps        []PanelAppStats
}

// PanelStatsResult holds the full panel stats page data.
type PanelStatsResult struct {
	Collectors []PanelCollectorStats
}

// GetPanelStats returns panel-wide stats for the stats page.
func (s *Service) GetPanelStats(ctx context.Context, req PanelStatsRequest) (PanelStatsResult, error) {
	access, err := s.userSvc.FindAccessPolicy(ctx, req.UserID)
	if err != nil {
		return PanelStatsResult{}, fmt.Errorf("could not load access for user %d: %w", req.UserID, err)
	}

	collectorNames, err := s.dbManager.ListCollectors()
	if err != nil {
		return PanelStatsResult{}, fmt.Errorf("could not list collectors: %w", err)
	}

	var result PanelStatsResult

	for _, collectorName := range collectorNames {
		if !access.AllowedTo(collectorName, "") {
			continue
		}

		cs := PanelCollectorStats{Name: collectorName}

		// Disk stats from collector open period
		// ponytail: O(apps) DB open + query in loop; fine for small deployments, batch if it matters
		handle, err := s.dbManager.GetCollectorDB(collectorName)
		if err == nil {
			disk, err := db.New(handle.Read()).GetOpenDiskPeriod(ctx)
			if err == nil {
				cs.DiskUsedGB = disk.UsedGb
				cs.DiskTotalGB = disk.TotalGb
			}
		}

		apps, err := s.ingestSvc.ListApps(collectorName)
		if err != nil {
			slog.Error("could not list apps for stats", "collector", collectorName, "error", err)
			continue
		}

		cs.Apps = make([]PanelAppStats, 0, len(apps))
		for _, appName := range apps {
			if !access.AllowedTo(collectorName, appName) {
				continue
			}

			pa := PanelAppStats{Name: appName}

			appHandle, err := s.dbManager.GetAppDB(collectorName, appName)
			if err != nil {
				slog.Error("could not open app db for stats", "collector", collectorName, "app", appName, "error", err)
				pa.IsStray = true
				cs.Apps = append(cs.Apps, pa)
				continue
			}

			appQueries := db.New(appHandle.Read())

			count, err := appQueries.CountLogsSince(ctx, req.Since.Unix())
			if err != nil {
				slog.Error("could not count logs for stats", "collector", collectorName, "app", appName, "error", err)
				pa.IsStray = true
				cs.Apps = append(cs.Apps, pa)
				continue
			}

			pa.LogCount = count
			pa.IsStray = count == 0

			// Compute percentiles from log volume time series (1h buckets)
			if count > 0 {
				pa.Percentile = computePercentiles(ctx, appQueries, req.Since)
			}

			cs.Apps = append(cs.Apps, pa)
		}

		result.Collectors = append(result.Collectors, cs)
	}

	return result, nil
}

// computePercentiles calculates p50/p75/p95/p99/max/min from hourly log volume buckets.
func computePercentiles(ctx context.Context, queries *db.Queries, since time.Time) PanelPercentiles {
	rows, err := queries.GetLogVolumeTimeSeries(ctx, db.GetLogVolumeTimeSeriesParams{
		Granularity: "1hour",
		Since:       since.Unix(),
	})
	if err != nil {
		slog.Error("could not get log volume time series for percentiles", "error", err)
		return PanelPercentiles{}
	}
	if len(rows) == 0 {
		return PanelPercentiles{}
	}

	// Aggregate counts per bucket (merge levels)
	bucketCounts := make(map[string]int64)
	for _, row := range rows {
		bucketCounts[fmt.Sprintf("%v", row.TimeBucket)] += row.Count
	}

	counts := make([]int64, 0, len(bucketCounts))
	for _, c := range bucketCounts {
		counts = append(counts, c)
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i] < counts[j] })

	return PanelPercentiles{
		P50: percentile(counts, 0.50),
		P75: percentile(counts, 0.75),
		P95: percentile(counts, 0.95),
		P99: percentile(counts, 0.99),
		Max: counts[len(counts)-1],
		Min: counts[0],
	}
}

func percentile(sorted []int64, p float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Round(p * float64(len(sorted)-1)))
	return sorted[idx]
}
