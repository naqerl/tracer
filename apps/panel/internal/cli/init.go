package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/auth"
	"tracer/build"
	"tracer/libs/installx"
)

// InitPanel creates the admin user and initial collector API key.
// It opens the database, resolves credentials from env vars if needed,
// checks if already initialized, creates the admin user, and generates
// a collector API key.
// Returns ErrAlreadyInitialized if users already exist and Force is not set.
// When Force is set and users already exist, the password of the given admin
// user is overwritten instead.
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

	// Load (or create) the master key used to encrypt API key plaintext at rest.
	keySecret, err := auth.LoadKeySecret(config.DataDir())
	if err != nil {
		return InitResult{}, fmt.Errorf("load key secret: %w", err)
	}

	// Ensure data dir files are owned by the service user.
	// -init may run as root during installation, but the panel service
	// runs as a non-root user and needs write access to the database.
	if err := installx.ChownDirRecursive(config.DataDir(), build.PanelUser, build.TracerGroup); err != nil {
		return InitResult{}, fmt.Errorf("chown data dir: %w", err)
	}

	return initWithHandle(ctx, handle, keySecret, username, password, isNewPassword, opts.Force)
}

// initWithHandle runs the init logic against an already-open database handle.
func initWithHandle(ctx context.Context, handle *dbmanager.DBHandle, keySecret []byte, username, password string, isNewPassword, force bool) (InitResult, error) {
	authSvc := auth.New(handle, keySecret)

	registerReq := auth.RegisterRequest{
		Username: username,
		Password: password,
		IsAdmin:  true,
	}
	if err := registerReq.Validate(); err != nil {
		return InitResult{}, fmt.Errorf("validate admin user request: %w", err)
	}

	// Check if users already exist
	hasUsers, err := authSvc.HasAnyUsers(ctx)
	if err != nil {
		return InitResult{}, fmt.Errorf("check users: %w", err)
	}

	if !hasUsers {
		// Fresh panel: create the admin user and a collector API key.
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

	if !force {
		return InitResult{}, ErrAlreadyInitialized
	}

	// Force mode: overwrite the password of the given admin user.
	err = authSvc.ResetPassword(ctx, username, password)
	if errors.Is(err, auth.ErrUserNotFound) {
		// The user does not exist yet — create them as admin.
		if _, err := authSvc.Register(ctx, registerReq); err != nil {
			return InitResult{}, fmt.Errorf("create admin user: %w", err)
		}
		return InitResult{
			AdminUsername: username,
			AdminPassword: password,
			IsNewPassword: isNewPassword,
		}, nil
	}
	if err != nil {
		return InitResult{}, fmt.Errorf("reset admin password: %w", err)
	}

	return InitResult{
		AdminUsername: username,
		AdminPassword: password,
		IsNewPassword: isNewPassword,
	}, nil
}
