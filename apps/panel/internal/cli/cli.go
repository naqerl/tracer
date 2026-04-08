// Package cli provides reusable CLI operations for the panel.
package cli

import "errors"

// Errors returned by CLI operations.
var (
	ErrAlreadyInitialized = errors.New("panel already initialized")
	ErrNoUsers            = errors.New("no users found")
)

// InitResult holds the result of panel initialization.
type InitResult struct {
	AdminUsername   string
	AdminPassword   string // Only populated when auto-generated
	CollectorAPIKey string
	IsNewPassword   bool // true if password was auto-generated
}

// InitOptions configures panel initialization.
// If Username or Password are empty, env vars TRACER_ADMIN_USERNAME and
// TRACER_ADMIN_PASSWORD will be used. If those are empty, Username defaults
// to "admin" and Password is auto-generated.
type InitOptions struct {
	Username string
	Password string
}
