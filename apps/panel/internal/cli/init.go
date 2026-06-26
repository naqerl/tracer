package cli

import (
	"context"
	"fmt"
	"os"

	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/auth"
	"tracer/pkg/setup"
)

// InitPanel creates the admin user and initial collector API key.
// It opens the database, resolves credentials from env vars if needed,
// checks if already initialized, creates the admin user, and generates
// a collector API key.
// Returns ErrAlreadyInitialized if users already exist.
func InitPanel(ctx context.Context, opts InitOptions) (InitResult, error) {
	// Resolve credentials from env vars if not provided
	username := opts.Username
	if username == "" {
		username = os.Getenv("TRACER_ADMIN_USERNAME")
		if username == "" {
			username = "admin"
		}
	}

	password := opts.Password
	isNewPassword := false
	if password == "" {
		password = os.Getenv("TRACER_ADMIN_PASSWORD")
		if password == "" {
			// Auto-generate password
			var err error
			password, err = auth.GenerateAPIKey()
			if err != nil {
				return InitResult{}, fmt.Errorf("generate password: %w", err)
			}
			password = password[:16] // Take first 16 chars for readability
			isNewPassword = true
		}
	}

	// Open database with migrations
	handle, err := dbmanager.OpenPanelDB(config.DataDir())
	if err != nil {
		return InitResult{}, fmt.Errorf("open panel database: %w", err)
	}
	defer handle.Close()

	// Ensure data dir files are owned by the service user.
	// -init may run as root during installation, but the panel service
	// runs as a non-root user and needs write access to the database.
	if err := setup.ChownDirRecursive(config.DataDir(), setup.PanelUser, setup.TracerGroup); err != nil {
		return InitResult{}, fmt.Errorf("chown data dir: %w", err)
	}

	authSvc := auth.New(handle)

	// Check if users already exist
	hasUsers, err := authSvc.HasAnyUsers(ctx)
	if err != nil {
		return InitResult{}, fmt.Errorf("check users: %w", err)
	}

	if hasUsers {
		return InitResult{}, ErrAlreadyInitialized
	}

	// Create admin user
	registerReq := auth.RegisterRequest{
		Username: username,
		Password: password,
		IsAdmin:  true,
	}
	if err := registerReq.Validate(); err != nil {
		return InitResult{}, fmt.Errorf("validate admin user request: %w", err)
	}

	if _, err := authSvc.Register(ctx, registerReq); err != nil {
		return InitResult{}, fmt.Errorf("create admin user: %w", err)
	}

	// Generate collector API key
	apiKeyReq := auth.GenerateCollectorAPIKeyRequest{Name: "default"}
	if err := apiKeyReq.Validate(); err != nil {
		return InitResult{}, fmt.Errorf("validate collector api key request: %w", err)
	}

	apiKey, _, err := authSvc.GenerateCollectorAPIKey(ctx, apiKeyReq)
	if err != nil {
		return InitResult{}, fmt.Errorf("generate collector api key: %w", err)
	}

	return InitResult{
		AdminUsername:   username,
		AdminPassword:   password,
		CollectorAPIKey: apiKey,
		IsNewPassword:   isNewPassword,
	}, nil
}
