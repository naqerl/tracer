package dbmanager

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"

	"github.com/ai-shift/tracer/db"
)

type Manager struct {
	mu      sync.RWMutex
	baseDir string
	dbs     map[string]*sql.DB
	queries map[string]*db.Queries
}

func New(baseDir string) (*Manager, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("create base dir: %w", err)
	}

	return &Manager{
		baseDir: baseDir,
		dbs:     make(map[string]*sql.DB),
		queries: make(map[string]*db.Queries),
	}, nil
}

// GetDB returns or creates a database connection for the given app
func (m *Manager) GetDB(appName string) (*sql.DB, error) {
	m.mu.RLock()
	if db, ok := m.dbs[appName]; ok {
		m.mu.RUnlock()
		return db, nil
	}
	m.mu.RUnlock()

	return m.createDB(appName)
}

// GetQueries returns SQLC queries for the given app
func (m *Manager) GetQueries(appName string) (*db.Queries, error) {
	m.mu.RLock()
	if q, ok := m.queries[appName]; ok {
		m.mu.RUnlock()
		return q, nil
	}
	m.mu.RUnlock()

	sqlDB, err := m.GetDB(appName)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if q, ok := m.queries[appName]; ok {
		return q, nil
	}

	q := db.New(sqlDB)
	m.queries[appName] = q
	return q, nil
}

func (m *Manager) createDB(appName string) (*sql.DB, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring lock
	if db, ok := m.dbs[appName]; ok {
		return db, nil
	}

	dbPath := filepath.Join(m.baseDir, fmt.Sprintf("%s.db", appName))
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database for app %s: %w", appName, err)
	}

	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	// Run migrations for new database
	if err := m.runMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run migrations for app %s: %w", appName, err)
	}

	m.dbs[appName] = sqlDB
	slog.Info("Created database for app", "app_name", appName, "path", dbPath)

	return sqlDB, nil
}

func (m *Manager) runMigrations(sqlDB *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		trace_id TEXT NOT NULL DEFAULT '',
		level TEXT NOT NULL,
		message TEXT NOT NULL,
		source TEXT NOT NULL,
		context TEXT DEFAULT '{}',
		raw TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_logs_trace_id ON logs(trace_id) WHERE trace_id != '';
	CREATE INDEX IF NOT EXISTS idx_logs_level ON logs(level);
	CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);
	CREATE INDEX IF NOT EXISTS idx_logs_source ON logs(source);
	`
	_, err := sqlDB.Exec(schema)
	return err
}

// ListApps returns all apps with databases
func (m *Manager) ListApps() ([]string, error) {
	entries, err := os.ReadDir(m.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var apps []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".db" {
			// Skip WAL and SHM files
			name := entry.Name()
			if filepath.Ext(name) == ".db" {
				appName := name[:len(name)-3] // Remove .db
				apps = append(apps, appName)
			}
		}
	}
	return apps, nil
}

// Close closes all database connections
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for appName, db := range m.dbs {
		if err := db.Close(); err != nil {
			slog.Error("Failed to close database", "app_name", appName, "error", err)
		}
	}

	m.dbs = make(map[string]*sql.DB)
	m.queries = make(map[string]*db.Queries)
	return nil
}
