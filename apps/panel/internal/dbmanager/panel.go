package dbmanager

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"git.naqerl.com/goext/rwdb"
	"github.com/pressly/goose/v3"

	panelmigrations "tracer/apps/panel/sql/panel"
)

// OpenPanelDB opens the panel database (panel.db) with proper pragmas and runs migrations.
func OpenPanelDB(dataDir string) (*rwdb.DBHandle, error) {
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

	return rwdb.New(sqlDB), nil
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
