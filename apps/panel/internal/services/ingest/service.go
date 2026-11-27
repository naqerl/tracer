package ingest

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"slices"
	"sync"
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

// ResourceConfigResolver resolves per-collector resource monitoring config,
// falling back to panel-wide globals when the collector has no override.
type ResourceConfigResolver struct {
	dbManager DBManager
	globals   ResourceConfig
}

// NewResourceConfigResolver creates a resolver that reads per-collector
// config from the stored collector config and falls back to globals.
func NewResourceConfigResolver(dbManager DBManager, globals ResourceConfig) *ResourceConfigResolver {
	return &ResourceConfigResolver{dbManager: dbManager, globals: globals}
}

// ResolveResourceConfig returns the resource monitoring parameters for a
// collector. Per-collector overrides from the stored config take precedence;
// zero/nil values fall back to the panel-wide globals.
func (r *ResourceConfigResolver) ResolveResourceConfig(collector string) ResourceConfig {
	cfg := r.globals

	handle, err := r.dbManager.GetCollectorDB(collector)
	if err != nil {
		return cfg
	}

	raw, err := db.New(handle.Read()).GetCollectorConfig(context.Background())
	if err != nil || raw == "" {
		return cfg
	}

	var cc types.CollectorConfig
	if err := json.Unmarshal([]byte(raw), &cc); err != nil || cc.Stats == nil {
		return cfg
	}

	s := cc.Stats
	if s.CPUDeltaPercent != nil {
		cfg.CPUDeltaPercent = *s.CPUDeltaPercent
	}
	if s.MemDeltaKB != nil {
		cfg.MemDeltaKB = *s.MemDeltaKB
	}
	if s.DiskDeltaKB != nil {
		cfg.DiskDeltaKB = *s.DiskDeltaKB
	}
	if s.CPUThresholdPercent != nil {
		cfg.CPUThresholdPercent = *s.CPUThresholdPercent
	}
	if s.MemThresholdPercent != nil {
		cfg.MemThresholdPercent = *s.MemThresholdPercent
	}
	if s.DiskThresholdPercent != nil {
		cfg.DiskThresholdPercent = *s.DiskThresholdPercent
	}
	if s.ResourceNotifyMinutes != nil {
		cfg.ResourceNotifyMinutes = *s.ResourceNotifyMinutes
	}

	return cfg
}

// Service manages ingest operations using dbmanager for all DB access.
// IngestConfig provides runtime access to ingest-related config values.
type IngestConfig interface {
	GetCPUDeltaPercent() float64
	GetMemDeltaKB() int
	GetDiskDeltaKB() int
	GetSampleRate() float64
	GetCPUThresholdPercent() float64
	GetMemThresholdPercent() float64
	GetDiskThresholdPercent() float64
	GetResourceNotifyMinutes() int
}

// CollectorConfigResolver resolves per-collector resource monitoring config,
// falling back to panel-wide globals when the collector has no override.
type CollectorConfigResolver interface {
	ResolveResourceConfig(collector string) ResourceConfig
}

// ResourceConfig holds the resolved resource monitoring parameters for a
// collector: per-collector overrides applied, globals used as defaults.
type ResourceConfig struct {
	CPUDeltaPercent       float64
	MemDeltaKB            int
	DiskDeltaKB           int
	CPUThresholdPercent   float64
	MemThresholdPercent   float64
	DiskThresholdPercent  float64
	ResourceNotifyMinutes int
}

type Service struct {
	dbManager      DBManager
	notifyChan     chan<- notify.Notification
	alertFormatter func(collectorName, appName string, logID int64) string
	cfg            IngestConfig
	resolver       CollectorConfigResolver

	mu             sync.Mutex
	lastNotified   map[string]int64            // collector/resource -> last notify unix time
	lastStatsAt    map[string]int64            // collector -> last stats sample unix time
	downNotifiedAt map[string]int64            // collector/app -> start of the alerted down period
	appDrops       map[string]map[string]int64 // collector -> app -> dropped unparseable lines
}

// New creates a new ingest service. A nil notifyChan or alertFormatter is
// replaced with a safe no-op so notifications can always be attempted.
func New(dbManager DBManager, notifyChan chan<- notify.Notification, alertFormatter func(collectorName, appName string, logID int64) string, cfg IngestConfig, resolver CollectorConfigResolver) *Service {
	if notifyChan == nil {
		notifyChan = make(chan notify.Notification)
	}
	if alertFormatter == nil {
		alertFormatter = func(_, _ string, _ int64) string { return "" }
	}
	return &Service{
		dbManager:      dbManager,
		notifyChan:     notifyChan,
		alertFormatter: alertFormatter,
		cfg:            cfg,
		resolver:       resolver,
		lastNotified:   make(map[string]int64),
		lastStatsAt:    make(map[string]int64),
		downNotifiedAt: make(map[string]int64),
		appDrops:       make(map[string]map[string]int64),
	}
}

// resolveResourceConfig returns the resource monitoring parameters for a
// collector. If a per-collector resolver is configured and has overrides,
// those are used; otherwise the panel-wide globals apply.
func (s *Service) resolveResourceConfig(collector string) ResourceConfig {
	if s.resolver != nil {
		return s.resolver.ResolveResourceConfig(collector)
	}
	return ResourceConfig{
		CPUDeltaPercent:       s.cfg.GetCPUDeltaPercent(),
		MemDeltaKB:            s.cfg.GetMemDeltaKB(),
		DiskDeltaKB:           s.cfg.GetDiskDeltaKB(),
		CPUThresholdPercent:   s.cfg.GetCPUThresholdPercent(),
		MemThresholdPercent:   s.cfg.GetMemThresholdPercent(),
		DiskThresholdPercent:  s.cfg.GetDiskThresholdPercent(),
		ResourceNotifyMinutes: s.cfg.GetResourceNotifyMinutes(),
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

// SystemStat represents the latest combined system statistics from open periods.
type SystemStat struct {
	CPUUsagePercent float64
	MemTotalMb      int64
	MemUsedMb       int64
	MemAvailableMb  int64
	DiskTotalGb     int64
	DiskUsedGb      int64
	DiskAvailGb     int64
	Timestamp       int64 // most recent stats sample, including unchanged values
}

// StatsHistoryResult holds period data for all three resource types.
type StatsHistoryResult struct {
	CPUPeriods  []db.CpuPeriod
	MemPeriods  []db.MemPeriod
	DiskPeriods []db.DiskPeriod
}

// SaveSystemStatsRequest contains system statistics to save for a collector.
type SaveSystemStatsRequest struct {
	CollectorName string
	Stats         types.Stats
}

func (r SaveSystemStatsRequest) Validate() error {
	return domain.ValidateCollectorName(r.CollectorName)
}

// SaveSystemStats saves system statistics for a collector using change-based periods.
// Creates a new period only when the value changes from the current open period.
// If the value is unchanged, the existing period continues (no INSERT).
// After saving, checks resource thresholds and sends notifications for breaches.
func (s *Service) SaveSystemStats(ctx context.Context, req SaveSystemStatsRequest) error {
	handle, err := s.dbManager.GetCollectorDB(req.CollectorName)
	if err != nil {
		return err
	}
	nowUnix := req.Stats.Timestamp.Unix()

	rcfg := s.resolveResourceConfig(req.CollectorName)

	err = handle.Write(ctx, func(tx db.DBTX) error {
		q := db.New(tx)

		// --- CPU ---
		if err := s.saveCPUPeriod(ctx, q, req.CollectorName, req.Stats.CPUUsagePercent, nowUnix, rcfg.CPUDeltaPercent); err != nil {
			return err
		}

		// --- Memory ---
		if err := s.saveMemPeriod(ctx, q, req.CollectorName, req.Stats.MemTotalMB, req.Stats.MemUsedMB, req.Stats.MemAvailableMB, nowUnix, rcfg.MemDeltaKB); err != nil {
			return err
		}

		// --- Disk ---
		if err := s.saveDiskPeriod(ctx, q, req.CollectorName, req.Stats.DiskTotalGB, req.Stats.DiskUsedGB, req.Stats.DiskAvailGB, nowUnix, rcfg.DiskDeltaKB); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.lastStatsAt[req.CollectorName] = nowUnix
	if len(req.Stats.AppDrops) > 0 {
		apps := s.appDrops[req.CollectorName]
		if apps == nil {
			apps = make(map[string]int64)
			s.appDrops[req.CollectorName] = apps
		}
		for app, drop := range req.Stats.AppDrops {
			apps[app] += drop.DroppedLines
		}
	}
	s.mu.Unlock()

	s.checkResourceThresholds(req.CollectorName, req.Stats, nowUnix, rcfg)
	return nil
}

// AppDroppedLines returns the unparseable lines dropped by an app since the
// panel last started.
func (s *Service) AppDroppedLines(collector, app string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appDrops[collector][app]
}

func (s *Service) saveCPUPeriod(ctx context.Context, q *db.Queries, collectorName string, usagePercent float64, nowUnix int64, deltaPercent float64) error {
	current, err := q.GetOpenCPUPeriod(ctx)
	switch {
	case err == nil:
		delta := usagePercent - current.UsagePercent
		if delta < 0 {
			delta = -delta
		}
		if delta < deltaPercent {
			return nil // change below threshold, keep period open
		}
		if err := q.CloseCPUPeriod(ctx, db.CloseCPUPeriodParams{
			EndedAt: sql.NullInt64{Int64: nowUnix, Valid: true},
			ID:      current.ID,
		}); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}

	_, err = q.InsertCPUPeriod(ctx, db.InsertCPUPeriodParams{
		CollectorName: collectorName,
		UsagePercent:  usagePercent,
		StartedAt:     nowUnix,
	})
	return err
}

func (s *Service) saveMemPeriod(ctx context.Context, q *db.Queries, collectorName string, totalMB, usedMB, availableMB int64, nowUnix int64, deltaKB int) error {
	current, err := q.GetOpenMemPeriod(ctx)
	switch {
	case err == nil:
		// Convert MB to KB for delta comparison
		totalKB := totalMB * 1024
		usedKB := usedMB * 1024
		availKB := availableMB * 1024
		curTotalKB := current.TotalMb * 1024
		curUsedKB := current.UsedMb * 1024
		curAvailKB := current.AvailableMb * 1024
		dTotal := totalKB - curTotalKB
		dUsed := usedKB - curUsedKB
		dAvail := availKB - curAvailKB
		if dTotal < 0 {
			dTotal = -dTotal
		}
		if dUsed < 0 {
			dUsed = -dUsed
		}
		if dAvail < 0 {
			dAvail = -dAvail
		}
		if dTotal < int64(deltaKB) && dUsed < int64(deltaKB) && dAvail < int64(deltaKB) {
			return nil // all changes below threshold, keep period open
		}
		if err := q.CloseMemPeriod(ctx, db.CloseMemPeriodParams{
			EndedAt: sql.NullInt64{Int64: nowUnix, Valid: true},
			ID:      current.ID,
		}); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}

	_, err = q.InsertMemPeriod(ctx, db.InsertMemPeriodParams{
		CollectorName: collectorName,
		TotalMb:       totalMB,
		UsedMb:        usedMB,
		AvailableMb:   availableMB,
		StartedAt:     nowUnix,
	})
	return err
}

func (s *Service) saveDiskPeriod(ctx context.Context, q *db.Queries, collectorName string, totalGB, usedGB, availGB int64, nowUnix int64, deltaKB int) error {
	current, err := q.GetOpenDiskPeriod(ctx)
	switch {
	case err == nil:
		// Convert GB to KB for delta comparison
		totalKB := totalGB * 1024 * 1024
		usedKB := usedGB * 1024 * 1024
		availKB := availGB * 1024 * 1024
		curTotalKB := current.TotalGb * 1024 * 1024
		curUsedKB := current.UsedGb * 1024 * 1024
		curAvailKB := current.AvailGb * 1024 * 1024
		dTotal := totalKB - curTotalKB
		dUsed := usedKB - curUsedKB
		dAvail := availKB - curAvailKB
		if dTotal < 0 {
			dTotal = -dTotal
		}
		if dUsed < 0 {
			dUsed = -dUsed
		}
		if dAvail < 0 {
			dAvail = -dAvail
		}
		if dTotal < int64(deltaKB) && dUsed < int64(deltaKB) && dAvail < int64(deltaKB) {
			return nil // all changes below threshold, keep period open
		}
		if err := q.CloseDiskPeriod(ctx, db.CloseDiskPeriodParams{
			EndedAt: sql.NullInt64{Int64: nowUnix, Valid: true},
			ID:      current.ID,
		}); err != nil {
			return err
		}
	case errors.Is(err, sql.ErrNoRows):
	default:
		return err
	}

	_, err = q.InsertDiskPeriod(ctx, db.InsertDiskPeriodParams{
		CollectorName: collectorName,
		TotalGb:       totalGB,
		UsedGb:        usedGB,
		AvailGb:       availGB,
		StartedAt:     nowUnix,
	})
	return err
}

// checkResourceThresholds evaluates CPU, memory, and disk usage against
// configured thresholds and sends a notification for each breach.
func (s *Service) checkResourceThresholds(collectorName string, stats types.Stats, nowUnix int64, rcfg ResourceConfig) {
	var breached []string

	memUsedPct := 0.0
	if stats.MemTotalMB > 0 {
		memUsedPct = float64(stats.MemUsedMB) / float64(stats.MemTotalMB) * 100
	}
	diskUsedPct := 0.0
	if stats.DiskTotalGB > 0 {
		diskUsedPct = float64(stats.DiskUsedGB) / float64(stats.DiskTotalGB) * 100
	}

	if cpuThreshold := rcfg.CPUThresholdPercent; cpuThreshold > 0 && stats.CPUUsagePercent >= cpuThreshold {
		breached = append(breached, "cpu")
	}
	if memThreshold := rcfg.MemThresholdPercent; memThreshold > 0 && memUsedPct >= memThreshold {
		breached = append(breached, "mem")
	}
	if diskThreshold := rcfg.DiskThresholdPercent; diskThreshold > 0 && diskUsedPct >= diskThreshold {
		breached = append(breached, "disk")
	}

	if s.suppressResourceNotify(collectorName, breached, nowUnix, rcfg.ResourceNotifyMinutes) {
		return
	}

	entry := notify.ResourceEntry{
		CPUUsagePercent: stats.CPUUsagePercent,
		MemUsedPercent:  memUsedPct,
		MemTotalMB:      stats.MemTotalMB,
		MemUsedMB:       stats.MemUsedMB,
		MemAvailableMB:  stats.MemAvailableMB,
		DiskUsedPercent: diskUsedPct,
		DiskTotalGB:     stats.DiskTotalGB,
		DiskUsedGB:      stats.DiskUsedGB,
		DiskAvailGB:     stats.DiskAvailGB,
		Breached:        breached,
		Timestamp:       nowUnix,
	}

	select {
	case s.notifyChan <- notify.Notification{
		Type:          notify.TypeResource,
		CollectorName: collectorName,
		Payload:       entry,
	}:
	default:
		slog.Error("Notification channel full, dropping resource notification")
	}
}

// suppressResourceNotify reports whether a resource notification should be
// skipped for a collector: true when nothing is breached, or when every
// breached resource is still within its cooldown. Resources no longer in
// breach are re-armed so their next breach notifies immediately.
func (s *Service) suppressResourceNotify(collectorName string, breached []string, nowUnix int64, notifyMinutes int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Re-arm recovered resources so their next breach notifies immediately.
	for _, res := range []string{"cpu", "mem", "disk"} {
		if !slices.Contains(breached, res) {
			delete(s.lastNotified, collectorName+"/"+res)
		}
	}
	if len(breached) == 0 {
		return true
	}

	cooldown := int64(notifyMinutes) * 60
	for _, res := range breached {
		last, ok := s.lastNotified[collectorName+"/"+res]
		if !ok || cooldown <= 0 || nowUnix-last >= cooldown {
			s.lastNotified[collectorName+"/"+res] = nowUnix
			return false
		}
	}
	return true
}

// GetLatestStats returns the latest system stats for a collector by combining
// the open periods from each resource table. Timestamp is the most recent
// sample, including samples that did not open a new period.
// Returns sql.ErrNoRows if no data exists for any resource.
func (s *Service) GetLatestStats(ctx context.Context, collector string) (SystemStat, error) {
	handle, err := s.dbManager.GetCollectorDB(collector)
	if err != nil {
		return SystemStat{}, err
	}
	queries := db.New(handle.Read())

	var stat SystemStat
	var maxTs int64
	var found bool

	cpu, err := queries.GetOpenCPUPeriod(ctx)
	if err == nil {
		stat.CPUUsagePercent = cpu.UsagePercent
		if cpu.StartedAt > maxTs {
			maxTs = cpu.StartedAt
		}
		found = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return SystemStat{}, err
	}

	mem, err := queries.GetOpenMemPeriod(ctx)
	if err == nil {
		stat.MemTotalMb = mem.TotalMb
		stat.MemUsedMb = mem.UsedMb
		stat.MemAvailableMb = mem.AvailableMb
		if mem.StartedAt > maxTs {
			maxTs = mem.StartedAt
		}
		found = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return SystemStat{}, err
	}

	disk, err := queries.GetOpenDiskPeriod(ctx)
	if err == nil {
		stat.DiskTotalGb = disk.TotalGb
		stat.DiskUsedGb = disk.UsedGb
		stat.DiskAvailGb = disk.AvailGb
		if disk.StartedAt > maxTs {
			maxTs = disk.StartedAt
		}
		found = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return SystemStat{}, err
	}

	if !found {
		return SystemStat{}, sql.ErrNoRows
	}

	s.mu.Lock()
	if ts, ok := s.lastStatsAt[collector]; ok && ts > maxTs {
		maxTs = ts
	}
	s.mu.Unlock()

	stat.Timestamp = maxTs
	return stat, nil
}

// GetStatsHistory returns period data for all three resource types since a given time.
// Returns error if collector DB doesn't exist.
func (s *Service) GetStatsHistory(ctx context.Context, collector string, since time.Time) (StatsHistoryResult, error) {
	handle, err := s.dbManager.GetCollectorDB(collector)
	if err != nil {
		return StatsHistoryResult{}, err
	}
	queries := db.New(handle.Read())

	sinceUnix := sql.NullInt64{Int64: since.UTC().Unix(), Valid: true}

	cpuPeriods, err := queries.GetCPUHistory(ctx, sinceUnix)
	if err != nil {
		return StatsHistoryResult{}, err
	}

	memPeriods, err := queries.GetMemHistory(ctx, sinceUnix)
	if err != nil {
		return StatsHistoryResult{}, err
	}

	diskPeriods, err := queries.GetDiskHistory(ctx, sinceUnix)
	if err != nil {
		return StatsHistoryResult{}, err
	}

	return StatsHistoryResult{
		CPUPeriods:  cpuPeriods,
		MemPeriods:  memPeriods,
		DiskPeriods: diskPeriods,
	}, nil
}

// DeleteOldStats removes stats older than the given time for all collectors.
// Skips collectors that don't have a DB yet.
func (s *Service) DeleteOldStats(ctx context.Context, before time.Time) error {
	collectors, err := s.ListCollectors()
	if err != nil {
		return err
	}

	beforeUnix := sql.NullInt64{Int64: before.UTC().Unix(), Valid: true}

	for _, collector := range collectors {
		handle, err := s.dbManager.GetCollectorDB(collector)
		if err != nil {
			if errors.Is(err, dbmanager.ErrDBNotFound) {
				continue
			}
			return err
		}
		if err := handle.Write(ctx, func(tx db.DBTX) error {
			q := db.New(tx)
			if err := q.DeleteOldCPUPeriods(ctx, beforeUnix); err != nil {
				return err
			}
			if err := q.DeleteOldMemPeriods(ctx, beforeUnix); err != nil {
				return err
			}
			return q.DeleteOldDiskPeriods(ctx, beforeUnix)
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
	if !isError && rand.Float64() >= s.cfg.GetSampleRate() {
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

	if isError {
		s.sendLogNotification(req, notify.TypeAlert, logID, logGroupID)
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

	select {
	case s.notifyChan <- notify.Notification{
		Type:          notify.TypeLifecycle,
		CollectorName: req.CollectorName,
		AppName:       req.AppName,
		Payload:       string(req.Kind),
	}:
	default:
		slog.Error("Notification channel full, dropping lifecycle notification")
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

	var prev types.HealthStatus
	var periodStartedAt int64

	err = handle.Write(ctx, func(tx db.DBTX) error {
		q := db.New(tx)

		if err := q.SaveHealthCheck(ctx, db.SaveHealthCheckParams{
			Status: string(req.Status),
			At:     req.Time.Unix(),
		}); err != nil {
			return err
		}

		current, err := q.GetOpenHealthPeriod(ctx)
		switch {
		case err == nil:
			periodStartedAt = current.StartedAt
			if current.Status == string(req.Status) {
				return nil
			}
			prev = types.HealthStatus(current.Status)
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

		periodStartedAt = req.Time.Unix()
		return q.SaveHealthPeriod(ctx, db.SaveHealthPeriodParams{
			Status:    string(req.Status),
			StartedAt: req.Time.Unix(),
			EndedAt:   sql.NullInt64{},
		})
	})
	if err != nil {
		return err
	}

	s.notifyHealth(ctx, req, prev, periodStartedAt)
	return nil
}

// notifyHealth sends an uptime notification for a health update. A down app is
// alerted once its configured delay has elapsed, and at most once per down
// period; every other status change alerts immediately.
func (s *Service) notifyHealth(ctx context.Context, req StoreHealthRequest, prev types.HealthStatus, periodStartedAt int64) {
	key := req.CollectorName + "/" + req.AppName

	if req.Status == types.HealthStatusDown {
		elapsed := req.Time.Sub(time.Unix(periodStartedAt, 0))
		if elapsed < s.uptimeAlertDelay(ctx, req.CollectorName, req.AppName) {
			return
		}

		s.mu.Lock()
		if s.downNotifiedAt == nil {
			s.downNotifiedAt = make(map[string]int64)
		}
		notifiedAt, notified := s.downNotifiedAt[key]
		if notified && notifiedAt == periodStartedAt {
			s.mu.Unlock()
			return
		}
		select {
		case s.notifyChan <- notify.Notification{
			Type:          notify.TypeUptime,
			CollectorName: req.CollectorName,
			AppName:       req.AppName,
			Payload: notify.UptimeEntry{
				Status:    req.Status,
				Previous:  prev,
				Timestamp: req.Time.Unix(),
			},
		}:
			s.downNotifiedAt[key] = periodStartedAt
		default:
			slog.Error("Notification channel full, dropping uptime notification")
		}
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	_, wasNotified := s.downNotifiedAt[key]
	delete(s.downNotifiedAt, key)
	s.mu.Unlock()

	// Announce only recoveries from a down that was notified; a blip held
	// back by alert_delay must not produce an up on its own.
	if !wasNotified {
		return
	}
	if prev != req.Status {
		select {
		case s.notifyChan <- notify.Notification{
			Type:          notify.TypeUptime,
			CollectorName: req.CollectorName,
			AppName:       req.AppName,
			Payload: notify.UptimeEntry{
				Status:    req.Status,
				Previous:  prev,
				Timestamp: req.Time.Unix(),
			},
		}:
		default:
			slog.Error("Notification channel full, dropping uptime notification")
		}
	}
}

// uptimeAlertDelay reads how long an app must stay down before its uptime
// alert is sent. A missing or malformed setting alerts immediately.
func (s *Service) uptimeAlertDelay(ctx context.Context, collectorName, appName string) time.Duration {
	handle, err := s.dbManager.GetCollectorDB(collectorName)
	if err != nil {
		slog.Error("failed to open collector database for uptime alert delay", "collector", collectorName, "error", err)
		return 0
	}

	raw, err := db.New(handle.Read()).GetCollectorConfig(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0
	case err != nil:
		slog.Error("failed to read collector config for uptime alert delay", "collector", collectorName, "error", err)
		return 0
	}
	if raw == "" {
		return 0
	}

	var cfg types.CollectorConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		slog.Error("failed to decode collector config for uptime alert delay", "collector", collectorName, "error", err)
		return 0
	}
	app, ok := cfg.GetApp(appName)
	if !ok {
		return 0
	}
	delay, err := app.UptimeAlertDelay()
	if err != nil {
		slog.Error("invalid uptime alert delay", "collector", collectorName, "app", appName, "error", err)
		return 0
	}
	return delay
}

// sendLogNotification sends a log-derived notification with its structured
// LogEntry payload; notifiers render their own presentation.
func (s *Service) sendLogNotification(req StoreLogRequest, typ notify.NotificationType, logID, groupID int64) {
	log := notify.LogEntry{
		Level:     req.Entry.Level,
		Message:   req.Entry.Message,
		GroupID:   groupID,
		LogID:     logID,
		CreatedAt: req.Entry.Timestamp,
		LogURL:    s.alertFormatter(req.CollectorName, req.AppName, logID),
	}

	select {
	case s.notifyChan <- notify.Notification{
		Type:          typ,
		CollectorName: req.CollectorName,
		AppName:       req.AppName,
		Payload:       log,
	}:
	default:
		slog.Error("Notification channel full, dropping notification")
	}
}
