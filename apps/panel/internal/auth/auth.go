package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ai-shift/tracer/apps/panel/db"
	userpkg "github.com/ai-shift/tracer/apps/panel/internal/user"
)

const (
	bcryptCost       = 12
	sessionTokenSize = 32
	apiKeySize       = 32
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrSessionExpired     = errors.New("session expired")
)

type Service struct {
	db              *sql.DB
	queries         *db.Queries
	sessionDuration time.Duration
}

func New(sqlDB *sql.DB, sessionDuration time.Duration) *Service {
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	return &Service{
		db:              sqlDB,
		queries:         db.New(sqlDB),
		sessionDuration: sessionDuration,
	}
}

func (s *Service) Close() error {
	return s.db.Close()
}

func (s *Service) DB() *sql.DB {
	return s.db
}

// Password hashing

func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func CheckPassword(password, hash string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil {
		if errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
			return ErrInvalidCredentials
		}
		return err
	}
	return nil
}

// Token generation

func GenerateToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func GenerateAPIKey() (string, error) {
	return GenerateToken(apiKeySize)
}

// Register creates a new user with hashed password
func (s *Service) Register(ctx context.Context, username, password string, isAdmin bool) (*db.User, error) {
	hash, err := HashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	var adminInt int64
	if isAdmin {
		adminInt = 1
	}

	user, err := s.queries.CreateUser(ctx, db.CreateUserParams{
		Username:     username,
		PasswordHash: hash,
		IsAdmin:      adminInt,
	})
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	slog.Info("User created", "username", username, "is_admin", isAdmin)
	return &user, nil
}

// Session management

func (s *Service) CreateSession(ctx context.Context, userID int64) (string, error) {
	token, err := GenerateToken(sessionTokenSize)
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}

	expiresAt := time.Now().Add(s.sessionDuration)
	err = s.queries.CreateSession(ctx, db.CreateSessionParams{
		ID:        token,
		UserID:    userID,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	return token, nil
}

func (s *Service) ValidateSession(ctx context.Context, token string) (*db.User, error) {
	session, err := s.queries.GetSession(ctx, token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionExpired
		}
		return nil, err
	}

	if time.Now().After(session.ExpiresAt) {
		s.queries.DeleteSession(ctx, token)
		return nil, ErrSessionExpired
	}

	user, err := s.queries.GetUserByID(ctx, session.UserID)
	if err != nil {
		return nil, fmt.Errorf("get user for session: %w", err)
	}

	return &user, nil
}

func (s *Service) DeleteSession(ctx context.Context, token string) error {
	return s.queries.DeleteSession(ctx, token)
}

func (s *Service) CleanupExpiredSessions(ctx context.Context) error {
	return s.queries.DeleteExpiredSessions(ctx, time.Now())
}

// Login

func (s *Service) Login(ctx context.Context, username, password string, userService *userpkg.Service) (*db.User, string, error) {
	user, err := userService.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, userpkg.ErrNotFound) {
			return nil, "", ErrInvalidCredentials
		}
		return nil, "", err
	}

	if err := CheckPassword(password, user.PasswordHash); err != nil {
		return nil, "", err
	}

	token, err := s.CreateSession(ctx, user.ID)
	if err != nil {
		return nil, "", err
	}

	slog.Info("User logged in", "username", username)
	return user, token, nil
}

// API Key management

const apiKeyConfigKey = "api_key"

func (s *Service) GetAPIKey(ctx context.Context) (string, error) {
	value, err := s.queries.GetConfig(ctx, apiKeyConfigKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return value, nil
}

func (s *Service) SetAPIKey(ctx context.Context, key string) error {
	return s.queries.SetConfig(ctx, db.SetConfigParams{
		Key:   apiKeyConfigKey,
		Value: key,
	})
}

func (s *Service) EnsureAPIKey(ctx context.Context) (string, error) {
	key, err := s.GetAPIKey(ctx)
	if err != nil {
		return "", err
	}

	if key != "" {
		return key, nil
	}

	// Generate new API key
	key, err = GenerateAPIKey()
	if err != nil {
		return "", fmt.Errorf("generate API key: %w", err)
	}

	if err := s.SetAPIKey(ctx, key); err != nil {
		return "", fmt.Errorf("save API key: %w", err)
	}

	slog.Info("Generated new API key")
	return key, nil
}

func (s *Service) ValidateAPIKey(ctx context.Context, key string) (bool, error) {
	storedKey, err := s.GetAPIKey(ctx)
	if err != nil {
		return false, err
	}
	return storedKey != "" && storedKey == key, nil
}

// User API Key management (LLM compatibility)

func (s *Service) GenerateUserAPIKey(ctx context.Context, userID int64, name string) (*db.UserApiKey, error) {
	key, err := GenerateToken(apiKeySize)
	if err != nil {
		return nil, fmt.Errorf("generate user API key: %w", err)
	}

	userKey, err := s.queries.CreateUserAPIKey(ctx, db.CreateUserAPIKeyParams{
		UserID: userID,
		ApiKey: key,
		Name:   name,
	})
	if err != nil {
		return nil, fmt.Errorf("create user API key: %w", err)
	}

	slog.Info("User API key created", "user_id", userID, "name", name)
	return &userKey, nil
}

func (s *Service) ValidateUserAPIKey(ctx context.Context, key string, userService *userpkg.Service) (*db.User, error) {
	userKey, err := s.queries.GetUserAPIKey(ctx, key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	// Update last used timestamp (fire and forget)
	go func() {
		_ = s.queries.UpdateUserAPIKeyLastUsed(context.Background(), db.UpdateUserAPIKeyLastUsedParams{
			LastUsedAt: sql.NullTime{Time: time.Now(), Valid: true},
			ApiKey:     key,
		})
	}()

	user, err := userService.GetByID(ctx, userKey.UserID)
	if err != nil {
		return nil, fmt.Errorf("get user for API key: %w", err)
	}

	return user, nil
}

func (s *Service) ListUserAPIKeys(ctx context.Context, userID int64) ([]db.UserApiKey, error) {
	return s.queries.GetUserAPIKeysByUser(ctx, userID)
}

func (s *Service) DeleteUserAPIKey(ctx context.Context, id int64) error {
	return s.queries.DeleteUserAPIKey(ctx, id)
}
