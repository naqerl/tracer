package dbmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/ai-shift/tracer/db"
	appmigrations "github.com/ai-shift/tracer/sql/app/migrations"
)

type Manager struct {
	mu            sync.RWMutex
	collectorsDir string
	dbs           map[string]*sql.DB
	queries       map[string]*db.Queries
}

func New(baseDir string) (*Manager, error) {
	collectorsDir := filepath.Join(baseDir, "collectors")
	if err := os.MkdirAll(collectorsDir, 0755); err != nil {
		return nil, fmt.Errorf("create collectors dir: %w", err)
	}

	return &Manager{
		collectorsDir: collectorsDir,
		dbs:           make(map[string]*sql.DB),
		queries:       make(map[string]*db.Queries),
	}, nil
}

type AppInstance struct {
	CollectorName string
	AppName       string
}

// GetDB returns or creates a database connection for the given collector/app.
func (m *Manager) GetDB(collectorName, appName string) (*sql.DB, error) {
	if err := validateIdentifier(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}
	if err := validateIdentifier(appName); err != nil {
		return nil, fmt.Errorf("invalid app name: %w", err)
	}
	key := dbKey(collectorName, appName)

	m.mu.RLock()
	if db, ok := m.dbs[key]; ok {
		m.mu.RUnlock()
		return db, nil
	}
	m.mu.RUnlock()

	return m.createDB(collectorName, appName)
}

// GetQueries returns SQLC queries for the given collector/app.
func (m *Manager) GetQueries(collectorName, appName string) (*db.Queries, error) {
	if err := validateIdentifier(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}
	if err := validateIdentifier(appName); err != nil {
		return nil, fmt.Errorf("invalid app name: %w", err)
	}
	key := dbKey(collectorName, appName)

	m.mu.RLock()
	if q, ok := m.queries[key]; ok {
		m.mu.RUnlock()
		return q, nil
	}
	m.mu.RUnlock()

	sqlDB, err := m.GetDB(collectorName, appName)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if q, ok := m.queries[key]; ok {
		return q, nil
	}

	q := db.New(sqlDB)
	m.queries[key] = q
	return q, nil
}

func (m *Manager) createDB(collectorName, appName string) (*sql.DB, error) {
	key := dbKey(collectorName, appName)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring lock
	if db, ok := m.dbs[key]; ok {
		return db, nil
	}

	appsDir := filepath.Join(m.collectorsDir, collectorName, "apps")
	if err := os.MkdirAll(appsDir, 0755); err != nil {
		return nil, fmt.Errorf("create apps dir for collector %s: %w", collectorName, err)
	}

	dbPath := filepath.Join(appsDir, fmt.Sprintf("%s.db", appName))
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database for app %s/%s: %w", collectorName, appName, err)
	}

	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	// Run migrations for new database
	if err := m.runMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run migrations for app %s/%s: %w", collectorName, appName, err)
	}

	m.dbs[key] = sqlDB
	slog.Info("Created database for app", "collector_name", collectorName, "app_name", appName, "path", dbPath)

	return sqlDB, nil
}

func (m *Manager) runMigrations(sqlDB *sql.DB) error {
	gooseProvider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, appmigrations.Embed)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	if _, err := gooseProvider.Up(context.Background()); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// ListApps returns unique app names discovered across collector app databases.
func (m *Manager) ListApps() ([]string, error) {
	instances, err := m.ListAppInstances()
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	apps := make([]string, 0, len(instances))
	for _, inst := range instances {
		if _, ok := seen[inst.AppName]; ok {
			continue
		}
		seen[inst.AppName] = struct{}{}
		apps = append(apps, inst.AppName)
	}
	sort.Strings(apps)
	return apps, nil
}

// ListAppInstances returns all collector/app DB instances.
func (m *Manager) ListAppInstances() ([]AppInstance, error) {
	entries, err := os.ReadDir(m.collectorsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []AppInstance{}, nil
		}
		return nil, err
	}

	instances := make([]AppInstance, 0)
	for _, collector := range entries {
		if !collector.IsDir() {
			continue
		}
		collectorName := collector.Name()
		appsDir := filepath.Join(m.collectorsDir, collectorName, "apps")

		appEntries, err := os.ReadDir(appsDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}

		for _, entry := range appEntries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".db" {
				continue
			}
			appName := strings.TrimSuffix(entry.Name(), ".db")
			instances = append(instances, AppInstance{
				CollectorName: collectorName,
				AppName:       appName,
			})
		}
	}

	sort.Slice(instances, func(i, j int) bool {
		if instances[i].CollectorName == instances[j].CollectorName {
			return instances[i].AppName < instances[j].AppName
		}
		return instances[i].CollectorName < instances[j].CollectorName
	})
	return instances, nil
}

func dbKey(collectorName, appName string) string {
	return collectorName + "\x00" + appName
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
