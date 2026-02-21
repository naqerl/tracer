package collectordb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	collectormigrations "github.com/ai-shift/tracer/sql/collector/migrations"
)

type InsertSystemStatsParams struct {
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

type SystemStat struct {
	ID              int64
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

type AppCollector struct {
	AppName       string
	CollectorName string
	LastSeenAt    time.Time
}

type Manager struct {
	mu            sync.RWMutex
	collectorsDir string
	dbs           map[string]*sql.DB
}

func New(baseDir string) (*Manager, error) {
	collectorsDir := filepath.Join(baseDir, "collectors")
	if err := os.MkdirAll(collectorsDir, 0755); err != nil {
		return nil, fmt.Errorf("create collectors dir: %w", err)
	}

	return &Manager{
		collectorsDir: collectorsDir,
		dbs:           make(map[string]*sql.DB),
	}, nil
}

func (m *Manager) GetDB(collectorName string) (*sql.DB, error) {
	if err := validateIdentifier(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}

	m.mu.RLock()
	if dbConn, ok := m.dbs[collectorName]; ok {
		m.mu.RUnlock()
		return dbConn, nil
	}
	m.mu.RUnlock()

	return m.createDB(collectorName)
}

func (m *Manager) createDB(collectorName string) (*sql.DB, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if dbConn, ok := m.dbs[collectorName]; ok {
		return dbConn, nil
	}

	collectorDir := filepath.Join(m.collectorsDir, collectorName)
	if err := os.MkdirAll(collectorDir, 0755); err != nil {
		return nil, fmt.Errorf("create collector dir: %w", err)
	}

	dbPath := filepath.Join(collectorDir, "collector.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	dbConn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open collector db: %w", err)
	}

	dbConn.SetMaxOpenConns(1)
	dbConn.SetMaxIdleConns(1)

	if err := runMigrations(dbConn); err != nil {
		dbConn.Close()
		return nil, fmt.Errorf("run collector migrations: %w", err)
	}

	m.dbs[collectorName] = dbConn
	return dbConn, nil
}

func runMigrations(sqlDB *sql.DB) error {
	provider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, collectormigrations.Embed)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	if _, err := provider.Up(context.Background()); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

func (m *Manager) UpsertCollectorApp(ctx context.Context, collectorName, appName string) error {
	dbConn, err := m.GetDB(collectorName)
	if err != nil {
		return err
	}
	if err := validateIdentifier(appName); err != nil {
		return fmt.Errorf("invalid app name: %w", err)
	}

	_, err = dbConn.ExecContext(ctx, `
		INSERT INTO collector_apps (app_name, last_seen_at)
		VALUES (?, ?)
		ON CONFLICT(app_name) DO UPDATE SET last_seen_at = excluded.last_seen_at
	`, appName, time.Now())
	return err
}

func (m *Manager) ListAppCollectors(ctx context.Context) ([]AppCollector, error) {
	collectors, err := m.listCollectors()
	if err != nil {
		return nil, err
	}

	result := make([]AppCollector, 0)
	for _, collector := range collectors {
		dbConn, err := m.GetDB(collector)
		if err != nil {
			return nil, err
		}

		rows, err := dbConn.QueryContext(ctx, `SELECT app_name, last_seen_at FROM collector_apps ORDER BY app_name`)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var appName string
			var lastSeen time.Time
			if err := rows.Scan(&appName, &lastSeen); err != nil {
				rows.Close()
				return nil, err
			}
			result = append(result, AppCollector{
				AppName:       appName,
				CollectorName: collector,
				LastSeenAt:    lastSeen,
			})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].CollectorName == result[j].CollectorName {
			return result[i].AppName < result[j].AppName
		}
		return result[i].CollectorName < result[j].CollectorName
	})
	return result, nil
}

func (m *Manager) InsertSystemStats(ctx context.Context, collectorName string, p InsertSystemStatsParams) error {
	dbConn, err := m.GetDB(collectorName)
	if err != nil {
		return err
	}
	_, err = dbConn.ExecContext(ctx, `
		INSERT INTO system_stats (
			collector_name, cpu_usage_percent, mem_total_mb, mem_used_mb,
			mem_available_mb, disk_total_gb, disk_used_gb, disk_avail_gb, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, collectorName, p.CpuUsagePercent, p.MemTotalMb, p.MemUsedMb, p.MemAvailableMb, p.DiskTotalGb, p.DiskUsedGb, p.DiskAvailGb, p.CreatedAt)
	return err
}

func (m *Manager) GetLatestStats(ctx context.Context, collectorName string) (*SystemStat, error) {
	dbConn, err := m.GetDB(collectorName)
	if err != nil {
		return nil, err
	}

	row := dbConn.QueryRowContext(ctx, `
		SELECT id, collector_name, cpu_usage_percent, mem_total_mb, mem_used_mb, mem_available_mb, disk_total_gb, disk_used_gb, disk_avail_gb, created_at
		FROM system_stats
		ORDER BY created_at DESC
		LIMIT 1
	`)

	var stat SystemStat
	if err := row.Scan(
		&stat.ID,
		&stat.CollectorName,
		&stat.CpuUsagePercent,
		&stat.MemTotalMb,
		&stat.MemUsedMb,
		&stat.MemAvailableMb,
		&stat.DiskTotalGb,
		&stat.DiskUsedGb,
		&stat.DiskAvailGb,
		&stat.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &stat, nil
}

func (m *Manager) GetCollectorStatsHistory(ctx context.Context, collectorName string, since time.Time) ([]SystemStat, error) {
	dbConn, err := m.GetDB(collectorName)
	if err != nil {
		return nil, err
	}

	rows, err := dbConn.QueryContext(ctx, `
		SELECT id, collector_name, cpu_usage_percent, mem_total_mb, mem_used_mb, mem_available_mb, disk_total_gb, disk_used_gb, disk_avail_gb, created_at
		FROM system_stats
		WHERE created_at >= ?
		ORDER BY created_at ASC
	`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make([]SystemStat, 0)
	for rows.Next() {
		var stat SystemStat
		if err := rows.Scan(
			&stat.ID,
			&stat.CollectorName,
			&stat.CpuUsagePercent,
			&stat.MemTotalMb,
			&stat.MemUsedMb,
			&stat.MemAvailableMb,
			&stat.DiskTotalGb,
			&stat.DiskUsedGb,
			&stat.DiskAvailGb,
			&stat.CreatedAt,
		); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

func (m *Manager) DeleteOldStats(ctx context.Context, before time.Time) error {
	collectors, err := m.listCollectors()
	if err != nil {
		return err
	}

	for _, collector := range collectors {
		dbConn, err := m.GetDB(collector)
		if err != nil {
			return err
		}
		if _, err := dbConn.ExecContext(ctx, `DELETE FROM system_stats WHERE created_at < ?`, before); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) listCollectors() ([]string, error) {
	entries, err := os.ReadDir(m.collectorsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}

	collectors := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			collectors = append(collectors, entry.Name())
		}
	}
	sort.Strings(collectors)
	return collectors, nil
}

func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, dbConn := range m.dbs {
		_ = dbConn.Close()
	}
	m.dbs = make(map[string]*sql.DB)
	return nil
}

func validateIdentifier(v string) error {
	if v == "" {
		return errors.New("must not be empty")
	}
	if len(v) > 128 {
		return errors.New("must be <= 128 characters")
	}
	for _, r := range v {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf("unsupported character %q", r)
	}
	return nil
}
