package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"tracer/apps/panel/db"
	"tracer/apps/panel/internal/domain"
	userpkg "tracer/apps/panel/internal/services/user"
)

const (
	bcryptCost        = 12
	sessionTokenSize  = 32
	apiKeySize        = 32
	maxUsernameLength = 32
	maxPasswordLength = 72
	SessionDuration   = 24 * time.Hour
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrSessionExpired     = errors.New("session expired")
)

type Service struct {
	db db.DBTX
}

type RegisterRequest struct {
	Username string
	Password string
	IsAdmin  bool
}

func (r RegisterRequest) Validate() error {
	trimmed := strings.TrimSpace(r.Username)
	if trimmed == "" {
		return fmt.Errorf("username is required")
	}
	if trimmed != r.Username {
		return fmt.Errorf("username must not have leading or trailing spaces")
	}
	if len(trimmed) > maxUsernameLength {
		return fmt.Errorf("username must be <= %d characters", maxUsernameLength)
	}
	if r.Password == "" {
		return fmt.Errorf("password is required")
	}
	if len(r.Password) > maxPasswordLength {
		return fmt.Errorf("password must be <= %d characters", maxPasswordLength)
	}
	return nil
}

type GenerateCollectorAPIKeyRequest struct {
	Name string
}

func (r GenerateCollectorAPIKeyRequest) Validate() error {
	return domain.ValidateAPIKeyName(r.Name)
}

type GenerateUserAPIKeyRequest struct {
	UserID int64
	Name   string
}

func (r GenerateUserAPIKeyRequest) Validate() error {
	if err := domain.ValidateUserID(r.UserID); err != nil {
		return err
	}
	return domain.ValidateAPIKeyName(r.Name)
}

func New(sqlDB *sql.DB) *Service {
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	return &Service{db: sqlDB}
}

// Password hashing

func hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

func checkPassword(password, hash string) error {
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

func generateToken(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func GenerateAPIKey() (string, error) {
	return generateToken(apiKeySize)
}

// Register creates a new user with hashed password.
func (s *Service) Register(ctx context.Context, req RegisterRequest) (*db.User, error) {
	hash, err := hashPassword(req.Password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}

	var adminInt int64
	if req.IsAdmin {
		adminInt = 1
	}

	user, err := db.New(s.db).CreateUser(ctx, db.CreateUserParams{
		Username:     req.Username,
		PasswordHash: hash,
		IsAdmin:      adminInt,
	})
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	slog.Info("User created", "username", req.Username, "is_admin", req.IsAdmin)
	return &user, nil
}

func (s *Service) HasAnyUsers(ctx context.Context) (bool, error) {
	count, err := db.New(s.db).CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("check users existence: %w", err)
	}

	return count > 0, nil
}

// Session management

func (s *Service) CreateSession(ctx context.Context, userID int64) (string, error) {
	token, err := generateToken(sessionTokenSize)
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}

	expiresAt := time.Now().Add(SessionDuration)
	err = db.New(s.db).CreateSession(ctx, db.CreateSessionParams{
		ID:        token,
		UserID:    userID,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	return token, nil
}

func (s *Service) ValidateSession(ctx context.Context, token string) (int64, error) {
	session, err := db.New(s.db).GetSession(ctx, token)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrSessionExpired
	}
	if err != nil {
		return 0, fmt.Errorf("could not get data from db: %w", err)
	}

	if time.Now().After(session.ExpiresAt) {
		err = db.New(s.db).DeleteSession(ctx, token)
		if err != nil {
			return 0, fmt.Errorf("could not delete session: %w", err)
		}
		return 0, ErrSessionExpired
	}

	return session.UserID, nil
}

func (s *Service) DeleteSession(ctx context.Context, token string) error {
	return db.New(s.db).DeleteSession(ctx, token)
}

func (s *Service) CleanupExpiredSessions(ctx context.Context) error {
	return db.New(s.db).DeleteExpiredSessions(ctx, time.Now())
}

// Login

func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	queries := db.New(s.db)
	user, err := queries.FindUserCredsByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrInvalidCredentials
		}
		return "", err
	}

	if err := checkPassword(password, user.PasswordHash); err != nil {
		return "", err
	}

	token, err := s.CreateSession(ctx, user.ID)
	if err != nil {
		return "", err
	}

	slog.Info("User logged in", "username", username)
	return token, nil
}

// API Key management

func hashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func maskedAPIKeySuffix(key string) string {
	suffix := key
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	return "****" + suffix
}

func (s *Service) GenerateCollectorAPIKey(ctx context.Context, req GenerateCollectorAPIKeyRequest) (string, *db.CollectorApiKey, error) {
	key, err := generateToken(apiKeySize)
	if err != nil {
		return "", nil, fmt.Errorf("generate collector api key: %w", err)
	}

	row, err := db.New(s.db).CreateCollectorAPIKey(ctx, db.CreateCollectorAPIKeyParams{
		Name:      req.Name,
		KeyHash:   hashAPIKey(key),
		KeySuffix: key[max(0, len(key)-6):],
	})
	if err != nil {
		return "", nil, fmt.Errorf("create collector api key: %w", err)
	}

	slog.Info("Collector API key created", "name", req.Name)
	return key, &row, nil
}

func (s *Service) ListCollectorAPIKeys(ctx context.Context) ([]db.GetCollectorAPIKeysRow, error) {
	return db.New(s.db).GetCollectorAPIKeys(ctx)
}

func (s *Service) DeleteCollectorAPIKey(ctx context.Context, name string) error {
	return db.New(s.db).DeleteCollectorAPIKey(ctx, name)
}

func (s *Service) ValidateCollectorAPIKey(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, nil
	}

	rows, err := db.New(s.db).ValidateAndUpdateCollectorAPIKey(ctx, db.ValidateAndUpdateCollectorAPIKeyParams{
		LastUsedAt: sql.NullTime{Time: time.Now().UTC(), Valid: true},
		KeyHash:    hashAPIKey(key),
	})
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return false, nil
	}
	return true, nil
}

func (s *Service) GetCollectorAPIKeyDisplay(ctx context.Context) (string, error) {
	keys, err := s.ListCollectorAPIKeys(ctx)
	if err != nil {
		return "", err
	}
	if len(keys) == 0 {
		return "", nil
	}
	return maskedAPIKeySuffix(keys[0].KeySuffix), nil
}

// User API Key management (LLM compatibility)

func (s *Service) GenerateUserAPIKey(ctx context.Context, req GenerateUserAPIKeyRequest) (*db.UserApiKey, error) {
	key, err := generateToken(apiKeySize)
	if err != nil {
		return nil, fmt.Errorf("generate user API key: %w", err)
	}

	userKey, err := db.New(s.db).CreateUserAPIKey(ctx, db.CreateUserAPIKeyParams{
		UserID: req.UserID,
		ApiKey: key,
		Name:   req.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("create user API key: %w", err)
	}

	slog.Info("User API key created", "user_id", req.UserID, "name", req.Name)
	return &userKey, nil
}

func (s *Service) ValidateUserAPIKey(ctx context.Context, key string, userService *userpkg.Service) (int64, error) {
	// Validate key and update last_used_at in one query
	userID, err := db.New(s.db).ValidateAndUpdateUserAPIKey(ctx, db.ValidateAndUpdateUserAPIKeyParams{
		LastUsedAt: sql.NullTime{Time: time.Now(), Valid: true},
		ApiKey:     key,
	})

	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrInvalidCredentials
	}

	return userID, err
}

func (s *Service) ListUserAPIKeys(ctx context.Context, userID int64) ([]db.UserApiKey, error) {
	return db.New(s.db).GetUserAPIKeysByUser(ctx, userID)
}

func (s *Service) DeleteUserAPIKey(ctx context.Context, id int64) error {
	return db.New(s.db).DeleteUserAPIKey(ctx, id)
}
