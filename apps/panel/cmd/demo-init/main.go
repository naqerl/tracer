package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ai-shift/tracer/apps/panel/internal/auth"
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
	authSvc := auth.New(*dataDir, 24*time.Hour)
	defer authSvc.Close()

	hasUsers, err := authSvc.HasUsers(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to check users: %v\n", err)
		os.Exit(1)
	}

	if !hasUsers {
		if _, err := authSvc.CreateUser(ctx, *username, *password, true); err != nil {
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

	if *keyFile != "" {
		if err := os.WriteFile(*keyFile, []byte(apiKey), 0600); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write key file: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("wrote collector api key to %s\n", *keyFile)
	}

	fmt.Printf("collector_api_key=%s\n", apiKey)
}
