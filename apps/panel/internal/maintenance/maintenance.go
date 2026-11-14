package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	sqliteDriver "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
)

const (
	// defaultVacuumMinFreeShare is the free-page share that makes a database
	// rewrite worthwhile.
	defaultVacuumMinFreeShare = 0.25
	// defaultVacuumMaxBytes is the file size above which a database is
	// compacted even when its free share is lower, so a multi-gigabyte file
	// never hides its dead weight until it crosses the free-share threshold.
	defaultVacuumMaxBytes = 2 << 30

	defaultCheckpointRetries  = 3
	defaultCheckpointBackoff  = 250 * time.Millisecond
	defaultCheckpointInterval = 15 * time.Minute
	defaultVacuumInterval     = 24 * time.Hour

	resultKeySeparator = "\x00"
)

// Config controls how often maintenance runs and when a database is rewritten.
type Config struct {
	MinFreeShare       float64
	MaxBytes           int64
	CheckpointRetries  int
	CheckpointBackoff  time.Duration
	CheckpointInterval time.Duration
	VacuumInterval     time.Duration
}

// DefaultConfig returns the production maintenance policy.
func DefaultConfig() Config {
	return Config{
		MinFreeShare:       defaultVacuumMinFreeShare,
		MaxBytes:           defaultVacuumMaxBytes,
		CheckpointRetries:  defaultCheckpointRetries,
		CheckpointBackoff:  defaultCheckpointBackoff,
		CheckpointInterval: defaultCheckpointInterval,
		VacuumInterval:     defaultVacuumInterval,
	}
}

// Result describes one app database's maintenance outcome.
type Result struct {
	Collector      string
	App            string
	Path           string
	Pages          int64
	FreePages      int64
	BytesBefore    int64
	BytesAfter     int64
	WALBytesBefore int64
	WALBytesAfter  int64
	ReclaimedBytes int64
	Vacuumed       bool
	Busy           bool
	At             time.Time
	Elapsed        time.Duration
}

// StorageStats describes an app database's on-disk size and free pages.
type StorageStats struct {
	Pages     int64
	FreePages int64
	DBBytes   int64
	WALBytes  int64
	FreeBytes int64
}

type Service struct {
	dbManager *dbmanager.Manager
	cfg       Config

	passMu sync.Mutex

	mu   sync.RWMutex
	last map[string]Result
}

// New creates a maintenance service for the manager's app databases.
func New(dbManager *dbmanager.Manager, cfg Config) *Service {
	return &Service{
		dbManager: dbManager,
		cfg:       cfg,
		last:      make(map[string]Result),
	}
}

// Start runs a full pass immediately, then a checkpoint pass on the short
// interval and a full checkpoint plus vacuum pass on the long interval.
func (s *Service) Start(ctx context.Context) {
	slog.Info("Maintenance service started",
		"checkpoint_interval", s.cfg.CheckpointInterval,
		"vacuum_interval", s.cfg.VacuumInterval,
		"vacuum_min_free_share", s.cfg.MinFreeShare,
		"vacuum_max_bytes", s.cfg.MaxBytes,
	)

	s.runPass(ctx, true)

	checkpointTicker := time.NewTicker(s.cfg.CheckpointInterval)
	vacuumTicker := time.NewTicker(s.cfg.VacuumInterval)
	defer checkpointTicker.Stop()
	defer vacuumTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("Maintenance service stopping")
			return
		case <-checkpointTicker.C:
			s.runPass(ctx, false)
		case <-vacuumTicker.C:
			s.runPass(ctx, true)
		}
	}
}

// RunOnce performs a full checkpoint and vacuum pass over every app database.
func (s *Service) RunOnce(ctx context.Context) {
	s.runPass(ctx, true)
}

// LastResult returns the most recent maintenance outcome for an app.
func (s *Service) LastResult(collectorName, appName string) (Result, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	res, ok := s.last[resultKey(collectorName, appName)]
	return res, ok
}

// Measure reads the on-disk size and free pages of an app database.
func Measure(ctx context.Context, handle *dbmanager.DBHandle, path string) (StorageStats, error) {
	pages, freePages, pageSize, err := readPageStats(ctx, handle.Read())
	if err != nil {
		return StorageStats{}, err
	}

	dbBytes, err := fileSize(path)
	if err != nil {
		return StorageStats{}, err
	}
	walBytes, err := fileSize(path + "-wal")
	if err != nil {
		return StorageStats{}, err
	}

	return StorageStats{
		Pages:     pages,
		FreePages: freePages,
		DBBytes:   dbBytes,
		WALBytes:  walBytes,
		FreeBytes: freePages * pageSize,
	}, nil
}

func (s *Service) runPass(ctx context.Context, vacuum bool) {
	s.passMu.Lock()
	defer s.passMu.Unlock()

	collectors, err := s.dbManager.ListCollectors()
	if err != nil {
		slog.Error("Could not list collectors for maintenance", "error", err)
		return
	}

	for _, collector := range collectors {
		apps, err := s.dbManager.ListApps(collector)
		if err != nil {
			slog.Error("Could not list apps for maintenance", "collector", collector, "error", err)
			continue
		}

		for _, app := range apps {
			res, err := s.runFile(ctx, collector, app, vacuum)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			if err != nil {
				slog.Error("App database maintenance failed", "collector", collector, "app", app, "error", err)
				continue
			}

			s.record(res)
			logResult(res)
		}
	}
}

func (s *Service) runFile(ctx context.Context, collectorName, appName string, vacuum bool) (Result, error) {
	start := time.Now()
	path := s.dbManager.AppDBPath(collectorName, appName)
	res := Result{Collector: collectorName, App: appName, Path: path}

	bytesBefore, err := fileSize(path)
	if err != nil {
		return res, err
	}
	walBytesBefore, err := fileSize(path + "-wal")
	if err != nil {
		return res, err
	}
	res.BytesBefore = bytesBefore
	res.WALBytesBefore = walBytesBefore

	handle, err := s.dbManager.GetAppDB(collectorName, appName)
	if err != nil {
		return res, fmt.Errorf("could not open app database: %w", err)
	}

	err = handle.Exclusive(ctx, func(tx db.DBTX) error {
		busy, err := s.checkpoint(ctx, tx)
		if err != nil {
			return err
		}
		if busy {
			res.Busy = true
			return nil
		}

		pages, freePages, pageSize, err := readPageStats(ctx, tx)
		if err != nil {
			return err
		}
		res.Pages = pages
		res.FreePages = freePages

		if !vacuum {
			return nil
		}

		bytesAtCheckpoint, err := fileSize(path)
		if err != nil {
			return err
		}
		if !shouldVacuum(pages, freePages, pageSize, bytesAtCheckpoint, s.cfg) {
			return nil
		}

		if _, err := tx.ExecContext(ctx, "VACUUM"); err != nil {
			return fmt.Errorf("could not vacuum database: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA optimize"); err != nil {
			return fmt.Errorf("could not optimize database: %w", err)
		}
		res.Vacuumed = true

		// VACUUM and optimize write through the WAL, so fold it again before
		// sizing the result.
		busy, err = s.checkpoint(ctx, tx)
		if err != nil {
			return err
		}
		res.Busy = busy
		return nil
	})
	res.At = time.Now()
	res.Elapsed = res.At.Sub(start)
	if err != nil {
		return res, err
	}

	res.BytesAfter, err = fileSize(path)
	if err != nil {
		return res, err
	}
	res.WALBytesAfter, err = fileSize(path + "-wal")
	if err != nil {
		return res, err
	}
	res.ReclaimedBytes = res.BytesBefore + res.WALBytesBefore - res.BytesAfter - res.WALBytesAfter

	return res, nil
}

// checkpoint folds and truncates the WAL, retrying a busy database a few times
// before giving up on this pass.
func (s *Service) checkpoint(ctx context.Context, tx db.DBTX) (bool, error) {
	for attempt := 0; attempt <= s.cfg.CheckpointRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(s.cfg.CheckpointBackoff):
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}

		var busy, logFrames, checkpointed int64
		err := tx.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed)
		if err != nil {
			if isBusy(err) {
				continue
			}
			return false, fmt.Errorf("could not checkpoint wal: %w", err)
		}
		if busy == 0 {
			return false, nil
		}
	}

	return true, nil
}

func (s *Service) record(res Result) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.last[resultKey(res.Collector, res.App)] = res
}

func readPageStats(ctx context.Context, tx db.DBTX) (pages, freePages, pageSize int64, err error) {
	if err := tx.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return 0, 0, 0, fmt.Errorf("could not read page count: %w", err)
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freePages); err != nil {
		return 0, 0, 0, fmt.Errorf("could not read freelist count: %w", err)
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return 0, 0, 0, fmt.Errorf("could not read page size: %w", err)
	}
	return pages, freePages, pageSize, nil
}

func shouldVacuum(pages, freePages, pageSize, dbBytes int64, cfg Config) bool {
	if pages <= 0 || pageSize <= 0 {
		return false
	}

	totalBytes := pages * pageSize
	freeBytes := freePages * pageSize
	if cfg.MinFreeShare > 0 && float64(freeBytes)/float64(totalBytes) >= cfg.MinFreeShare {
		return true
	}
	return cfg.MaxBytes > 0 && dbBytes > cfg.MaxBytes
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("could not stat %s: %w", path, err)
	}
	return info.Size(), nil
}

func isBusy(err error) bool {
	var sqliteErr *sqliteDriver.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code()&0xff == sqlite3.SQLITE_BUSY
	}
	return false
}

func logResult(res Result) {
	args := []any{
		"collector", res.Collector,
		"app", res.App,
		"path", res.Path,
		"pages", res.Pages,
		"free_pages", res.FreePages,
		"bytes_before", res.BytesBefore,
		"bytes_after", res.BytesAfter,
		"wal_before", res.WALBytesBefore,
		"wal_after", res.WALBytesAfter,
		"vacuumed", res.Vacuumed,
		"reclaimed_bytes", res.ReclaimedBytes,
		"elapsed", res.Elapsed,
	}
	if res.Busy {
		slog.Warn("App database maintenance skipped: database busy", args...)
		return
	}
	slog.Info("App database maintenance complete", args...)
}

func resultKey(collectorName, appName string) string {
	return collectorName + resultKeySeparator + appName
}
