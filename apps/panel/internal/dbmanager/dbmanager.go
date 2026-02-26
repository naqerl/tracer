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

	"tracer/apps/panel/db"
	appmigrations "tracer/apps/panel/sql/app"
	collectormigrations "tracer/apps/panel/sql/collector"
)

// ErrDBNotFound is returned when a database does not exist.
var ErrDBNotFound = errors.New("database not found")

type Manager struct {
	mu            sync.RWMutex
	collectorsDir string
	appDBs        map[string]*sql.DB // key: collector\x00app
	collectorDBs  map[string]*sql.DB // key: collector (for stats)
}

func New(baseDir string) (*Manager, error) {
	collectorsDir := filepath.Join(baseDir, "collectors")
	if err := os.MkdirAll(collectorsDir, 0755); err != nil {
		return nil, fmt.Errorf("create collectors dir: %w", err)
	}

	return &Manager{
		collectorsDir: collectorsDir,
		appDBs:        make(map[string]*sql.DB),
		collectorDBs:  make(map[string]*sql.DB),
	}, nil
}

// ListCollectors returns all collector names from filesystem.
func (m *Manager) ListCollectors() ([]string, error) {
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

// ListApps returns all app names for a specific collector.
func (m *Manager) ListApps(collector string) ([]string, error) {
	appsDir := filepath.Join(m.collectorsDir, collector, "apps")
	entries, err := os.ReadDir(appsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}

	apps := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".db" {
			continue
		}
		appName := strings.TrimSuffix(entry.Name(), ".db")
		apps = append(apps, appName)
	}
	sort.Strings(apps)
	return apps, nil
}

// GetAppDB returns the database connection for the given collector/app.
// Returns ErrDBNotFound if the database does not exist.
func (m *Manager) GetAppDB(collectorName, appName string) (db.DBTX, error) {
	if err := validateIdentifier(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}
	if err := validateIdentifier(appName); err != nil {
		return nil, fmt.Errorf("invalid app name: %w", err)
	}
	key := appDBKey(collectorName, appName)

	m.mu.RLock()
	if sqlDB, ok := m.appDBs[key]; ok {
		m.mu.RUnlock()
		return newLoggingDB(sqlDB, collectorName+"/"+appName), nil
	}
	m.mu.RUnlock()

	// Check if DB file exists on disk
	dbPath := filepath.Join(m.collectorsDir, collectorName, "apps", fmt.Sprintf("%s.db", appName))
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, ErrDBNotFound
	}

	// DB exists on disk but not in cache, open it
	return m.openAppDB(collectorName, appName)
}

// CreateAppDB creates a new database for the given collector/app.
func (m *Manager) CreateAppDB(collectorName, appName string) (db.DBTX, error) {
	if err := validateIdentifier(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}
	if err := validateIdentifier(appName); err != nil {
		return nil, fmt.Errorf("invalid app name: %w", err)
	}

	key := appDBKey(collectorName, appName)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Return existing if already created
	if sqlDB, ok := m.appDBs[key]; ok {
		return newLoggingDB(sqlDB, collectorName+"/"+appName), nil
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
	if err := m.runAppMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run migrations for app %s/%s: %w", collectorName, appName, err)
	}

	m.appDBs[key] = sqlDB
	slog.Info("Created database for app", "collector_name", collectorName, "app_name", appName, "path", dbPath)

	return newLoggingDB(sqlDB, collectorName+"/"+appName), nil
}

func (m *Manager) openAppDB(collectorName, appName string) (db.DBTX, error) {
	key := appDBKey(collectorName, appName)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring lock
	if sqlDB, ok := m.appDBs[key]; ok {
		return newLoggingDB(sqlDB, collectorName+"/"+appName), nil
	}

	dbPath := filepath.Join(m.collectorsDir, collectorName, "apps", fmt.Sprintf("%s.db", appName))
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database for app %s/%s: %w", collectorName, appName, err)
	}

	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	m.appDBs[key] = sqlDB
	return newLoggingDB(sqlDB, collectorName+"/"+appName), nil
}

// GetCollectorDB returns the stats database for a collector.
// Returns ErrDBNotFound if the database does not exist.
func (m *Manager) GetCollectorDB(collectorName string) (db.DBTX, error) {
	if err := validateIdentifier(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}

	m.mu.RLock()
	if sqlDB, ok := m.collectorDBs[collectorName]; ok {
		m.mu.RUnlock()
		return newLoggingDB(sqlDB, "collector/"+collectorName), nil
	}
	m.mu.RUnlock()

	// Check if DB file exists on disk
	dbPath := filepath.Join(m.collectorsDir, collectorName, "collector.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, ErrDBNotFound
	}

	// DB exists on disk but not in cache, open it
	return m.openCollectorDB(collectorName)
}

// CreateCollectorDB creates a new stats database for the collector.
func (m *Manager) CreateCollectorDB(collectorName string) (db.DBTX, error) {
	if err := validateIdentifier(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Return existing if already created
	if sqlDB, ok := m.collectorDBs[collectorName]; ok {
		return newLoggingDB(sqlDB, "collector/"+collectorName), nil
	}

	collectorDir := filepath.Join(m.collectorsDir, collectorName)
	if err := os.MkdirAll(collectorDir, 0755); err != nil {
		return nil, fmt.Errorf("create collector dir for %s: %w", collectorName, err)
	}

	dbPath := filepath.Join(collectorDir, "collector.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open collector db for %s: %w", collectorName, err)
	}

	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	// Run migrations for new database
	if err := m.runCollectorMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run migrations for collector %s: %w", collectorName, err)
	}

	m.collectorDBs[collectorName] = sqlDB
	slog.Info("Created collector database", "collector_name", collectorName, "path", dbPath)

	return newLoggingDB(sqlDB, "collector/"+collectorName), nil
}

func (m *Manager) openCollectorDB(collectorName string) (db.DBTX, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring lock
	if sqlDB, ok := m.collectorDBs[collectorName]; ok {
		return newLoggingDB(sqlDB, "collector/"+collectorName), nil
	}

	dbPath := filepath.Join(m.collectorsDir, collectorName, "collector.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open collector db for %s: %w", collectorName, err)
	}

	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	m.collectorDBs[collectorName] = sqlDB
	return newLoggingDB(sqlDB, "collector/"+collectorName), nil
}

func (m *Manager) runAppMigrations(sqlDB *sql.DB) error {
	gooseProvider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, appmigrations.Embed)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	if _, err := gooseProvider.Up(context.Background()); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

func (m *Manager) runCollectorMigrations(sqlDB *sql.DB) error {
	gooseProvider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, collectormigrations.Embed)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	if _, err := gooseProvider.Up(context.Background()); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

func appDBKey(collectorName, appName string) string {
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

	for appName, db := range m.appDBs {
		if err := db.Close(); err != nil {
			slog.Error("Failed to close app database", "app_name", appName, "error", err)
		}
	}

	for collectorName, db := range m.collectorDBs {
		if err := db.Close(); err != nil {
			slog.Error("Failed to close collector database", "collector_name", collectorName, "error", err)
		}
	}

	m.appDBs = make(map[string]*sql.DB)
	m.collectorDBs = make(map[string]*sql.DB)
	return nil
}
