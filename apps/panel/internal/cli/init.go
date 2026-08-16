package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/services/auth"
)

// InitPanel creates the admin user and initial collector API key.
// It resolves credentials from env vars if needed, checks if already
// initialized, creates the admin user, and generates a collector API key.
// authSvc is the already-constructed auth service; main.go builds it once with
// the production bcrypt cost and shares it across all CLI commands.
// Returns ErrAlreadyInitialized if users already exist and Force is not set.
// When Force is set and users already exist, the password of the given admin
// user is overwritten instead.
func InitPanel(ctx context.Context, authSvc *auth.Service, opts InitOptions) (InitResult, error) {
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

	return initWithHandle(ctx, authSvc, username, password, isNewPassword, opts.Force)
}

// initWithHandle runs the init logic against an already-constructed auth
// service. Production builds it with auth.DefaultBcryptCost; tests build it
// with a lower cost (e.g. bcrypt.MinCost) to keep the suite fast.
func initWithHandle(ctx context.Context, authSvc *auth.Service, username, password string, isNewPassword, force bool) (InitResult, error) {
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

		apiKey, err := authSvc.GenerateCollectorAPIKey(ctx, domain.System{}, apiKeyReq)
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
