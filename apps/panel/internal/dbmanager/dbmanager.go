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

	"tracer/apps/panel/internal/domain"
	appmigrations "tracer/apps/panel/sql/app"
	collectormigrations "tracer/apps/panel/sql/collector"
	panelmigrations "tracer/apps/panel/sql/panel"
)

// ErrDBNotFound is returned when a database does not exist.
var ErrDBNotFound = errors.New("database not found")

type Manager struct {
	mu            sync.RWMutex
	collectorsDir string
	appDBs        map[string]*DBHandle // key: collector\x00app
	appAliases    map[string]*DBHandle // renamed old key -> new handle
	collectorDBs  map[string]*DBHandle // key: collector (for stats)

	// dsnBuilder builds the DSN for a database path. Production uses
	// disk-backed WAL SQLite; tests override it with :memory: DSNs so the
	// suite does not pay per-test disk I/O.
	dsnBuilder func(dbPath string) string
}

func New(baseDir string) (*Manager, error) {
	collectorsDir := filepath.Join(baseDir, "collectors")
	if err := os.MkdirAll(collectorsDir, 0755); err != nil {
		return nil, fmt.Errorf("create collectors dir: %w", err)
	}

	return &Manager{
		collectorsDir: collectorsDir,
		appDBs:        make(map[string]*DBHandle),
		appAliases:    make(map[string]*DBHandle),
		collectorDBs:  make(map[string]*DBHandle),
		dsnBuilder:    buildDSN,
	}, nil
}

// NewWithDSN is a test-only constructor that overrides how SQLite DSNs are
// built. Tests use it to point all databases at :memory: instead of disk.
func NewWithDSN(baseDir string, dsn func(dbPath string) string) (*Manager, error) {
	m, err := New(baseDir)
	if err != nil {
		return nil, err
	}
	m.dsnBuilder = dsn
	return m, nil
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

// GetAppDB returns the DBHandle for the given collector/app.
// Returns ErrDBNotFound if the database does not exist.
func (m *Manager) GetAppDB(collectorName, appName string) (*DBHandle, error) {
	if err := domain.ValidateCollectorName(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}
	if err := domain.ValidateAppName(appName); err != nil {
		return nil, fmt.Errorf("invalid app name: %w", err)
	}
	key := appDBKey(collectorName, appName)

	m.mu.RLock()
	if h, ok := m.appDBs[key]; ok {
		m.mu.RUnlock()
		return h, nil
	}
	if h, ok := m.appAliases[key]; ok {
		m.mu.RUnlock()
		return h, nil
	}
	m.mu.RUnlock()

	// Check if DB file exists on disk
	dbPath := m.appDBPath(collectorName, appName)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, ErrDBNotFound
	}

	// DB exists on disk but not in cache, open it
	return m.openAppDB(collectorName, appName)
}

// CreateAppDB creates a new database for the given collector/app.
func (m *Manager) CreateAppDB(collectorName, appName string) (*DBHandle, error) {
	if err := domain.ValidateCollectorName(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}
	if err := domain.ValidateAppName(appName); err != nil {
		return nil, fmt.Errorf("invalid app name: %w", err)
	}

	key := appDBKey(collectorName, appName)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Return existing if already created
	if h, ok := m.appDBs[key]; ok {
		return h, nil
	}
	// A fresh app with this name supersedes any rename alias.
	delete(m.appAliases, key)

	appsDir := filepath.Join(m.collectorsDir, collectorName, "apps")
	if err := os.MkdirAll(appsDir, 0755); err != nil {
		return nil, fmt.Errorf("create apps dir for collector %s: %w", collectorName, err)
	}

	dbPath := m.appDBPath(collectorName, appName)
	sqlDB, err := openSQLiteDB(m.dsnBuilder(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open database for app %s/%s: %w", collectorName, appName, err)
	}

	h := NewDBHandle(sqlDB, collectorName+"/"+appName)

	// Run migrations for new database
	if err := m.runAppMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run migrations for app %s/%s: %w", collectorName, appName, err)
	}

	m.appDBs[key] = h
	slog.Info("Created database for app", "collector_name", collectorName, "app_name", appName, "path", dbPath)

	return h, nil
}

func (m *Manager) openAppDB(collectorName, appName string) (*DBHandle, error) {
	key := appDBKey(collectorName, appName)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring lock
	if h, ok := m.appDBs[key]; ok {
		return h, nil
	}

	dbPath := m.appDBPath(collectorName, appName)
	sqlDB, err := openSQLiteDB(m.dsnBuilder(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open database for app %s/%s: %w", collectorName, appName, err)
	}

	h := NewDBHandle(sqlDB, collectorName+"/"+appName)
	m.appDBs[key] = h
	return h, nil
}

// openUnlockedAppDB opens an existing app database without taking the manager
// lock; the caller must have already acquired it.
func (m *Manager) openUnlockedAppDB(collectorName, appName string) (*DBHandle, error) {
	dbPath := m.appDBPath(collectorName, appName)
	sqlDB, err := openSQLiteDB(m.dsnBuilder(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open database for app %s/%s: %w", collectorName, appName, err)
	}
	return NewDBHandle(sqlDB, collectorName+"/"+appName), nil
}

// openRenamedAppDB opens the freshly renamed app database and applies
// migrations. The caller must hold the manager lock.
func (m *Manager) openRenamedAppDB(collectorName, appName string) (*DBHandle, error) {
	h, err := m.openUnlockedAppDB(collectorName, appName)
	if err != nil {
		return nil, err
	}
	if err := m.runAppMigrations(h.pool); err != nil {
		h.Close()
		return nil, fmt.Errorf("run migrations for app %s/%s: %w", collectorName, appName, err)
	}
	return h, nil
}

// DeleteAppDB closes and removes the database for a collector's app,
// including WAL and shared-memory sidecars.
func (m *Manager) DeleteAppDB(collectorName, appName string) error {
	if err := domain.ValidateCollectorName(collectorName); err != nil {
		return fmt.Errorf("invalid collector name: %w", err)
	}
	if err := domain.ValidateAppName(appName); err != nil {
		return fmt.Errorf("invalid app name: %w", err)
	}

	key := appDBKey(collectorName, appName)

	m.mu.Lock()
	if h, ok := m.appDBs[key]; ok {
		if err := h.Close(); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("could not close app database %s/%s: %w", collectorName, appName, err)
		}
		delete(m.appDBs, key)
	}
	// An alias for a renamed app shares the new app's handle; only the
	// mapping is removed, never the handle.
	delete(m.appAliases, key)
	m.mu.Unlock()

	dbPath := m.appDBPath(collectorName, appName)
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("could not remove app database %s/%s: %w", collectorName, appName, err)
		}
	}
	slog.Info("Deleted database for app", "collector_name", collectorName, "app_name", appName, "path", dbPath)
	return nil
}

// RenameAppDB renames a collector's app database from oldName to newName. All
// writes queued before the call complete before any file is moved, so no
// in-flight log is lost or stranded in the old database. Logs that later
// arrive under the old name are routed to the renamed database through an
// alias until the name is reused or the app is deleted.
func (m *Manager) RenameAppDB(collectorName, oldName, newName string) (*DBHandle, error) {
	if err := domain.ValidateCollectorName(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}
	if err := domain.ValidateAppName(oldName); err != nil {
		return nil, fmt.Errorf("invalid app name: %w", err)
	}
	if err := domain.ValidateAppName(newName); err != nil {
		return nil, fmt.Errorf("invalid app name: %w", err)
	}

	oldKey := appDBKey(collectorName, oldName)
	newKey := appDBKey(collectorName, newName)

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.appDBs[newKey]; ok {
		return nil, fmt.Errorf("app %s/%s already exists", collectorName, newName)
	}
	newDBPath := m.appDBPath(collectorName, newName)
	if _, err := os.Stat(newDBPath); err == nil {
		return nil, fmt.Errorf("app database %s/%s already exists", collectorName, newName)
	}

	h, ok := m.appDBs[oldKey]
	if !ok {
		if _, err := os.Stat(m.appDBPath(collectorName, oldName)); errors.Is(err, os.ErrNotExist) {
			return nil, ErrDBNotFound
		}
		var err error
		h, err = m.openUnlockedAppDB(collectorName, oldName)
		if err != nil {
			return nil, err
		}
	}

	if err := h.DrainAndClose(); err != nil {
		return nil, fmt.Errorf("could not drain app database %s/%s: %w", collectorName, oldName, err)
	}
	delete(m.appDBs, oldKey)

	oldDBPath := m.appDBPath(collectorName, oldName)
	for _, ext := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(oldDBPath+ext, newDBPath+ext); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("could not rename app database %s/%s: %w", collectorName, oldName, err)
		}
	}

	newHandle, err := m.openRenamedAppDB(collectorName, newName)
	if err != nil {
		return nil, err
	}
	m.appDBs[newKey] = newHandle
	m.appAliases[oldKey] = newHandle
	slog.Info("Renamed database for app", "collector_name", collectorName, "old_app_name", oldName, "new_app_name", newName, "path", newDBPath)
	return newHandle, nil
}

// GetCollectorDB returns the stats DBHandle for a collector.
// Returns ErrDBNotFound if the database does not exist.
func (m *Manager) GetCollectorDB(collectorName string) (*DBHandle, error) {
	if err := domain.ValidateCollectorName(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}

	m.mu.RLock()
	if h, ok := m.collectorDBs[collectorName]; ok {
		m.mu.RUnlock()
		return h, nil
	}
	m.mu.RUnlock()

	// Check if DB file exists on disk
	dbPath := m.collectorDBPath(collectorName)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, ErrDBNotFound
	}

	// DB exists on disk but not in cache, open it
	return m.openCollectorDB(collectorName)
}

// CreateCollectorDB creates a new stats database for the collector.
func (m *Manager) CreateCollectorDB(collectorName string) (*DBHandle, error) {
	if err := domain.ValidateCollectorName(collectorName); err != nil {
		return nil, fmt.Errorf("invalid collector name: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Return existing if already created
	if h, ok := m.collectorDBs[collectorName]; ok {
		return h, nil
	}

	collectorDir := filepath.Join(m.collectorsDir, collectorName)
	if err := os.MkdirAll(collectorDir, 0755); err != nil {
		return nil, fmt.Errorf("create collector dir for %s: %w", collectorName, err)
	}

	dbPath := m.collectorDBPath(collectorName)
	sqlDB, err := openSQLiteDB(m.dsnBuilder(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open collector db for %s: %w", collectorName, err)
	}

	// Run migrations for new database
	if err := m.runCollectorMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run migrations for collector %s: %w", collectorName, err)
	}

	h := NewDBHandle(sqlDB, "collector/"+collectorName)
	m.collectorDBs[collectorName] = h
	slog.Info("Created collector database", "collector_name", collectorName, "path", dbPath)

	return h, nil
}

func (m *Manager) openCollectorDB(collectorName string) (*DBHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring lock
	if h, ok := m.collectorDBs[collectorName]; ok {
		return h, nil
	}

	dbPath := m.collectorDBPath(collectorName)
	sqlDB, err := openSQLiteDB(m.dsnBuilder(dbPath))
	if err != nil {
		return nil, fmt.Errorf("open collector db for %s: %w", collectorName, err)
	}

	h := NewDBHandle(sqlDB, "collector/"+collectorName)
	m.collectorDBs[collectorName] = h
	return h, nil
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

func (m *Manager) appDBPath(collectorName, appName string) string {
	return filepath.Join(m.collectorsDir, collectorName, "apps", fmt.Sprintf("%s.db", appName))
}

func (m *Manager) collectorDBPath(collectorName string) string {
	return filepath.Join(m.collectorsDir, collectorName, "collector.db")
}

func buildDSN(dbPath string) string {
	return fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)
}

func openSQLiteDB(dsn string) (*sql.DB, error) {
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	return sqlDB, nil
}

// RunMigrationsForAll runs migrations on all existing collector and app databases.
// This should be called on startup to ensure all databases are up to date.
func (m *Manager) RunMigrationsForAll() error {
	collectors, err := m.ListCollectors()
	if err != nil {
		return fmt.Errorf("list collectors: %w", err)
	}

	var errs []error
	for _, collector := range collectors {
		// Run collector DB migrations
		if _, err := m.GetCollectorDB(collector); err == nil {
			sqlDB, err := openSQLiteDB(m.dsnBuilder(m.collectorDBPath(collector)))
			if err != nil {
				errs = append(errs, fmt.Errorf("open collector db %s: %w", collector, err))
				continue
			}

			if err := m.runCollectorMigrations(sqlDB); err != nil {
				sqlDB.Close()
				errs = append(errs, fmt.Errorf("migrate collector %s: %w", collector, err))
				continue
			}
			sqlDB.Close()
			slog.Info("Migrated collector database", "collector", collector)
		}

		// Run app DB migrations
		apps, err := m.ListApps(collector)
		if err != nil {
			errs = append(errs, fmt.Errorf("list apps for %s: %w", collector, err))
			continue
		}

		for _, app := range apps {
			if _, err := m.GetAppDB(collector, app); err == nil {
				sqlDB, err := openSQLiteDB(m.dsnBuilder(m.appDBPath(collector, app)))
				if err != nil {
					errs = append(errs, fmt.Errorf("open app db %s/%s: %w", collector, app, err))
					continue
				}

				if err := m.runAppMigrations(sqlDB); err != nil {
					sqlDB.Close()
					errs = append(errs, fmt.Errorf("migrate app %s/%s: %w", collector, app, err))
					continue
				}
				sqlDB.Close()
				slog.Info("Migrated app database", "collector", collector, "app", app)
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("migration errors: %v", errs)
	}
	return nil
}

// Close closes all database connections
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for appName, handle := range m.appDBs {
		if err := handle.Close(); err != nil {
			slog.Error("Failed to close app database", "app_name", appName, "error", err)
		}
	}

	for collectorName, handle := range m.collectorDBs {
		if err := handle.Close(); err != nil {
			slog.Error("Failed to close collector database", "collector_name", collectorName, "error", err)
		}
	}

	m.appDBs = make(map[string]*DBHandle)
	m.collectorDBs = make(map[string]*DBHandle)
	return nil
}

// OpenPanelDB opens the panel database (panel.db) with proper pragmas and runs migrations.
// This is a package-level function (not tied to Manager) as panel.db is separate from collector/app DBs.
func OpenPanelDB(dataDir string) (*DBHandle, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "panel.db")
	dsn := buildDSN(dbPath)

	sqlDB, err := openSQLiteDB(dsn)
	if err != nil {
		return nil, fmt.Errorf("open panel database: %w", err)
	}

	if err := runPanelMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run panel migrations: %w", err)
	}

	return NewDBHandle(sqlDB, "panel"), nil
}

func runPanelMigrations(sqlDB *sql.DB) error {
	gooseProvider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, panelmigrations.Embed)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	if _, err := gooseProvider.Up(context.Background()); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
