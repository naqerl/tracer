package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
)

var (
	ErrNotFound     = errors.New("user not found")
	ErrAccessDenied = errors.New("access denied")
)

type Service struct {
	handle *dbmanager.DBHandle
}

func NewService(handle *dbmanager.DBHandle) *Service {
	return &Service{
		handle: handle,
	}
}

// FindByID returns the user with the given id. Admins and the system actor
// may read anyone; a user may read only themselves.
func (s *Service) FindByID(ctx context.Context, actor domain.Actor, id int64) (domain.User, error) {
	switch a := actor.(type) {
	case domain.System:
	case domain.User:
		if a.Role != domain.RoleAdmin && a.ID != id {
			return domain.User{}, ErrAccessDenied
		}
	default:
		return domain.User{}, ErrAccessDenied
	}

	row, err := db.New(s.handle.Read()).GetUserByID(ctx, id)
	switch {
	case err == nil:
		return db2dto(row), nil
	case errors.Is(err, sql.ErrNoRows):
		return domain.User{}, ErrNotFound
	default:
		return domain.User{}, fmt.Errorf("could not get user from db: %w", err)
	}
}

func (s *Service) FindAccessPolicy(ctx context.Context, userID int64) (AccessPolicy, error) {
	row, err := db.New(s.handle.Read()).GetUserByID(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessPolicy{}, ErrNotFound
	}
	if err != nil {
		return AccessPolicy{}, fmt.Errorf("could not get user from db: %w", err)
	}
	if row.IsAdmin == 1 {
		return AccessPolicy{}, nil
	}

	rows, err := db.New(s.handle.Read()).FindUserAccessPolicy(ctx, userID)
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

// List returns all users for admins and the system actor; a non-admin user
// sees only themselves and the admins.
func (s *Service) List(ctx context.Context, actor domain.Actor) ([]domain.User, error) {
	switch actor.(type) {
	case domain.System:
	case domain.User:
	default:
		return nil, ErrAccessDenied
	}

	rows, err := db.New(s.handle.Read()).ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	users := make([]domain.User, 0, len(rows))
	for _, row := range rows {
		u := db2dto(row)
		if a, ok := actor.(domain.User); ok && a.Role != domain.RoleAdmin && a.ID != u.ID && u.Role != domain.RoleAdmin {
			continue
		}
		users = append(users, u)
	}
	return users, nil
}

// Delete deletes a user by ID
func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.handle.Write(ctx, func(tx db.DBTX) error {
		return db.New(tx).DeleteUser(ctx, id)
	})
}

// AddApp grants a user access to an app within a collector.
func (s *Service) GrantAccess(ctx context.Context, userID int64, access AppAccess) error {
	return s.handle.Write(ctx, func(tx db.DBTX) error {
		return db.New(tx).GrantAppAccess(ctx, db.GrantAppAccessParams{
			UserID:        userID,
			CollectorName: access.CollectorName,
			AppName:       access.AppName,
		})
	})
}

// RemoveApp revokes a user's access to an app within a collector.
func (s *Service) RevokeAccess(ctx context.Context, userID int64, access AppAccess) error {
	return s.handle.Write(ctx, func(tx db.DBTX) error {
		return db.New(tx).RevokeAppAccess(ctx, db.RevokeAppAccessParams{
			UserID:        userID,
			CollectorName: access.CollectorName,
			AppName:       access.AppName,
		})
	})
}

// Create creates a new user (used by auth package)
func (s *Service) Create(ctx context.Context, username, passwordHash string, isAdmin bool) (domain.User, error) {
	var adminInt int64
	if isAdmin {
		adminInt = 1
	}

	var user db.User
	err := s.handle.Write(ctx, func(tx db.DBTX) error {
		result, err := db.New(tx).CreateUser(ctx, db.CreateUserParams{
			Username:     username,
			PasswordHash: passwordHash,
			IsAdmin:      adminInt,
			CreatedAt:    time.Now().UTC().Unix(),
		})
		if err != nil {
			return err
		}
		user = result
		return nil
	})
	if err != nil {
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}

	return db2dto(user), nil
}

func db2dto(r db.User) domain.User {
	role := domain.RoleUser
	if r.IsAdmin == 1 {
		role = domain.RoleAdmin
	}
	return domain.User{
		ID:        r.ID,
		Username:  r.Username,
		CreatedAt: r.CreatedAt,
		Role:      role,
	}
}
