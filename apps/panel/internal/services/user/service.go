package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"git.naqerl.com/goext/rwdb"

	"tracer/apps/panel/db"
)

var (
	ErrNotFound = errors.New("user not found")
)

type Service struct {
	handle *rwdb.DBHandle
}

func NewService(handle *rwdb.DBHandle) *Service {
	return &Service{
		handle: handle,
	}
}

func (s *Service) FindByID(ctx context.Context, id int64) (User, error) {
	user, err := db.New(s.handle.Read(ctx)).GetUserByID(ctx, id)
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

	rows, err := db.New(s.handle.Read(ctx)).FindUserAccessPolicy(ctx, userId)
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
	return db.New(s.handle.Read(ctx)).ListUsers(ctx)
}

// Delete deletes a user by ID
func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.handle.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		return db.New(tx).DeleteUser(wctx, id)
	})
}

// AddApp grants a user access to an app within a collector.
func (s *Service) GrantAccess(ctx context.Context, userID int64, access AppAccess) error {
	return s.handle.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		return db.New(tx).GrantAppAccess(wctx, db.GrantAppAccessParams{
			UserID:        userID,
			CollectorName: access.CollectorName,
			AppName:       access.AppName,
		})
	})
}

// RemoveApp revokes a user's access to an app within a collector.
func (s *Service) RevokeAccess(ctx context.Context, userID int64, access AppAccess) error {
	return s.handle.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		return db.New(tx).RevokeAppAccess(wctx, db.RevokeAppAccessParams{
			UserID:        userID,
			CollectorName: access.CollectorName,
			AppName:       access.AppName,
		})
	})
}

// Create creates a new user (used by auth package)
func (s *Service) Create(ctx context.Context, username, passwordHash string, isAdmin bool) (User, error) {
	var adminInt int64
	if isAdmin {
		adminInt = 1
	}

	var user db.User
	err := s.handle.Write(ctx, func(wctx context.Context, tx *sql.Tx) error {
		result, err := db.New(tx).CreateUser(wctx, db.CreateUserParams{
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
