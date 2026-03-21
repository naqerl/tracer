package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"tracer/apps/panel/db"
)

var (
	ErrNotFound = errors.New("user not found")
)

type Service struct {
	queries *db.Queries
}

func NewService(sqlDB *sql.DB) *Service {
	return &Service{
		queries: db.New(sqlDB),
	}
}

func (s *Service) FindByID(ctx context.Context, id int64) (User, error) {
	user, err := s.queries.GetUserByID(ctx, id)
	switch {
	case err == nil:
		return db2dto(user), nil
	case errors.Is(err, sql.ErrNoRows):
		return User{}, ErrNotFound
	default:
		return User{}, fmt.Errorf("could not get user from db: %w", err)
	}
}

func (s *Service) FindAccessPolicy(ctx context.Context, userId int64) (AccessPolicy, error) {
	user, err := s.FindByID(ctx, userId)
	if err != nil {
		return AccessPolicy{}, err
	}
	if user.IsAdmin {
		return AccessPolicy{}, nil
	}

	rows, err := s.queries.FindUserAccessPolicy(ctx, userId)
	if err != nil {
		return AccessPolicy{}, fmt.Errorf("could not get user access policy from db: %w", err)
	}

	apps := make([]AppAccess, 0, len(rows))
	for _, row := range rows {
		apps = append(apps, AppAccess{
			CollectorName: row.CollectorName,
			AppName:       row.AppName,
		})
	}

	return AccessPolicy{Apps: apps}, nil
}

// List returns all users
func (s *Service) List(ctx context.Context) ([]db.User, error) {
	return s.queries.ListUsers(ctx)
}

// Delete deletes a user by ID
func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.queries.DeleteUser(ctx, id)
}

// AddApp grants a user access to an app within a collector.
func (s *Service) GrantAccess(ctx context.Context, userID int64, access AppAccess) error {
	return s.queries.GrantAppAccess(ctx, db.GrantAppAccessParams{
		UserID:        userID,
		CollectorName: access.CollectorName,
		AppName:       access.AppName,
	})
}

// RemoveApp revokes a user's access to an app within a collector.
func (s *Service) RevokeAccess(ctx context.Context, userID int64, access AppAccess) error {
	return s.queries.RevokeAppAccess(ctx, db.RevokeAppAccessParams{
		UserID:        userID,
		CollectorName: access.CollectorName,
		AppName:       access.AppName,
	})
}

// Create creates a new user (used by auth package)
func (s *Service) Create(ctx context.Context, username, passwordHash string, isAdmin bool) (User, error) {
	var adminInt int64
	if isAdmin {
		adminInt = 1
	}

	user, err := s.queries.CreateUser(ctx, db.CreateUserParams{
		Username:     username,
		PasswordHash: passwordHash,
		IsAdmin:      adminInt,
		CreatedAt:    time.Now().UTC().Unix(),
	})
	if err != nil {
		return User{}, fmt.Errorf("create user: %w", err)
	}

	return db2dto(user), nil
}

func db2dto(r db.User) User {
	return User{
		ID:       r.ID,
		Username: r.Username,
		IsAdmin:  r.IsAdmin == 1,
	}
}
