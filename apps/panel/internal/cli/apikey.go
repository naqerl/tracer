package cli

import (
	"context"
	"fmt"

	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/services/auth"
)

// GenerateCollectorAPIKey creates a new collector API key with the given name.
// authSvc is the already-constructed auth service; main.go builds it once with
// the production bcrypt cost and shares it across all CLI commands.
func GenerateCollectorAPIKey(ctx context.Context, authSvc *auth.Service, name string) (string, error) {
	req := auth.GenerateCollectorAPIKeyRequest{Name: name}
	if err := req.Validate(); err != nil {
		return "", fmt.Errorf("validate collector api key request: %w", err)
	}

	created, err := authSvc.GenerateCollectorAPIKey(ctx, domain.System{}, req)
	if err != nil {
		return "", fmt.Errorf("generate collector api key: %w", err)
	}

	return created.Plaintext, nil
}
