package cli

import (
	"context"
	"fmt"

	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/auth"
)

// GenerateCollectorAPIKey creates a new collector API key with the given name.
func GenerateCollectorAPIKey(ctx context.Context, name string) (string, error) {
	handle, err := dbmanager.OpenPanelDB(config.DataDir())
	if err != nil {
		return "", fmt.Errorf("open panel database: %w", err)
	}
	defer handle.Close()

	authSvc := auth.New(handle)

	req := auth.GenerateCollectorAPIKeyRequest{Name: name}
	if err := req.Validate(); err != nil {
		return "", fmt.Errorf("validate collector api key request: %w", err)
	}

	apiKey, _, err := authSvc.GenerateCollectorAPIKey(ctx, req)
	if err != nil {
		return "", fmt.Errorf("generate collector api key: %w", err)
	}

	return apiKey, nil
}
