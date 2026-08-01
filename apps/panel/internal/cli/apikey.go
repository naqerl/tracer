package cli

import (
	"context"
	"fmt"

	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/auth"
	"tracer/build"
	"tracer/libs/installx"
)

// GenerateCollectorAPIKey creates a new collector API key with the given name.
func GenerateCollectorAPIKey(ctx context.Context, name string) (string, error) {
	handle, err := dbmanager.OpenPanelDB(config.DataDir())
	if err != nil {
		return "", fmt.Errorf("open panel database: %w", err)
	}
	defer handle.Close()

	// Load (or create) the master key used to encrypt API key plaintext at rest.
	keySecret, err := auth.LoadKeySecret(config.DataDir())
	if err != nil {
		return "", fmt.Errorf("load key secret: %w", err)
	}

	// Ensure data dir files (including the freshly created keys.key) are owned
	// by the service user. -generate-api-key may run as root during
	// installation, but the panel service runs as a non-root user and needs to
	// read the key file; otherwise startup would fail.
	if err := installx.ChownDirRecursive(config.DataDir(), build.PanelUser, build.TracerGroup); err != nil {
		return "", fmt.Errorf("chown data dir: %w", err)
	}

	authSvc := auth.New(handle, keySecret)

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
