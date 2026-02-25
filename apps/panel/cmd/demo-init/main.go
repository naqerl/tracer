package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/user"
	panelmigrations "tracer/apps/panel/sql/panel"
)

func main() {
	var (
		dataDir  = flag.String("data-dir", "/demo/data", "panel data directory")
		username = flag.String("username", "demo", "initial admin username")
		password = flag.String("password", "demo123", "initial admin password")
		keyFile  = flag.String("key-file", "", "optional file path to write collector API key")
	)
	flag.Parse()

	ctx := context.Background()

	// Initialize panel database and run migrations
	panelDB, err := initPanelDB(*dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize panel database: %v\n", err)
		os.Exit(1)
	}
	defer panelDB.Close()

	authSvc := auth.New(panelDB)
	defer authSvc.Close()

	userSvc := user.New(panelDB)

	hasUsers, err := userSvc.HasAny(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to check users: %v\n", err)
		os.Exit(1)
	}

	if !hasUsers {
		if _, err := authSvc.Register(ctx, *username, *password, true); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create initial admin user: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("created demo admin user: %s\n", *username)
	}

	apiKey, err := authSvc.EnsureAPIKey(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to ensure api key: %v\n", err)
		os.Exit(1)
	}

	// Ensure we always write a usable raw key for collectors.
	// EnsureAPIKey may return only a masked preview when a hashed key already exists.
	if *keyFile != "" && len(apiKey) >= 4 && apiKey[:4] == "****" {
		apiKey, err = auth.GenerateAPIKey()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to generate collector api key: %v\n", err)
			os.Exit(1)
		}
		if err := authSvc.SetAPIKey(ctx, apiKey); err != nil {
			fmt.Fprintf(os.Stderr, "failed to persist collector api key: %v\n", err)
			os.Exit(1)
		}
	}

	if *keyFile != "" {
		if err := os.WriteFile(*keyFile, []byte(apiKey), 0600); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write key file: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("wrote collector api key to %s\n", *keyFile)
	}

	fmt.Printf("collector_api_key=%s\n", apiKey)
}

func initPanelDB(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "panel.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open panel database: %w", err)
	}

	if err := runMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return sqlDB, nil
}

func runMigrations(sqlDB *sql.DB) error {
	gooseProvider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, panelmigrations.Embed)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	_, err = gooseProvider.Up(context.Background())
	if err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}
