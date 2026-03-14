package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"tracer/apps/panel/db"
)

var (
	ErrNotFound = errors.New("user not found")
)

type Service struct {
	queries *db.Queries
}

type AppAccess struct {
	CollectorName string
	AppName       string
}

func New(sqlDB *sql.DB) *Service {
	return &Service{
		queries: db.New(sqlDB),
	}
}

// GetByID returns a user by ID
func (s *Service) GetByID(ctx context.Context, id int64) (*db.User, error) {
	user, err := s.queries.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &user, nil
}

// GetByUsername returns a user by username
func (s *Service) GetByUsername(ctx context.Context, username string) (*db.User, error) {
	user, err := s.queries.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &user, nil
}

// List returns all users
func (s *Service) List(ctx context.Context) ([]db.User, error) {
	return s.queries.ListUsers(ctx)
}

// Delete deletes a user by ID
func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.queries.DeleteUser(ctx, id)
}

// HasAny returns true if at least one user exists
func (s *Service) HasAny(ctx context.Context) (bool, error) {
	count, err := s.queries.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetApps returns the collector-scoped apps a user has access to.
func (s *Service) GetApps(ctx context.Context, userID int64) ([]AppAccess, error) {
	rows, err := s.queries.GetUserApps(ctx, userID)
	if err != nil {
		return nil, err
	}

	apps := make([]AppAccess, 0, len(rows))
	for _, row := range rows {
		apps = append(apps, AppAccess{
			CollectorName: row.CollectorName,
			AppName:       row.AppName,
		})
	}
	return apps, nil
}

// AddApp grants a user access to an app within a collector.
func (s *Service) AddApp(ctx context.Context, userID int64, collectorName, appName string) error {
	return s.queries.AddUserApp(ctx, db.AddUserAppParams{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
	})
}

// RemoveApp revokes a user's access to an app within a collector.
func (s *Service) RemoveApp(ctx context.Context, userID int64, collectorName, appName string) error {
	return s.queries.RemoveUserApp(ctx, db.RemoveUserAppParams{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
	})
}

// HasAppAccess checks if a user has access to a specific collector app.
func (s *Service) HasAppAccess(ctx context.Context, userID int64, collectorName, appName string) (bool, error) {
	user, err := s.GetByID(ctx, userID)
	if err != nil {
		return false, err
	}

	// Admins have access to all apps
	if user.IsAdmin == 1 {
		return true, nil
	}

	apps, err := s.GetApps(ctx, userID)
	if err != nil {
		return false, err
	}

	for _, app := range apps {
		if app.CollectorName == collectorName && app.AppName == appName {
			return true, nil
		}
	}

	return false, nil
}

// Create creates a new user (used by auth package)
func (s *Service) Create(ctx context.Context, username, passwordHash string, isAdmin bool) (*db.User, error) {
	var adminInt int64
	if isAdmin {
		adminInt = 1
	}

	user, err := s.queries.CreateUser(ctx, db.CreateUserParams{
		Username:     username,
		PasswordHash: passwordHash,
		IsAdmin:      adminInt,
	})
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	return &user, nil
}
