// FIXME: AUTH PACKAGE is for AUTH, not notify, stats or collectors
// Decompose them into their own features and inject these services into handlers
package auth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"github.com/ai-shift/tracer/core/services/auth/migrations"
	"github.com/ai-shift/tracer/core/services/notify"
	"github.com/ai-shift/tracer/db"
)

const (
	bcryptCost       = 12
	sessionTokenSize = 32
	apiKeySize       = 32
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUserNotFound       = errors.New("user not found")
	ErrSessionExpired     = errors.New("session expired")
)

type Service struct {
	db              *sql.DB
	queries         *db.Queries
	sessionDuration time.Duration
}

// TODO: Panic (log.Fatalf) instead of error
func New(dataDir string, sessionDuration time.Duration) (*Service, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	dbPath := filepath.Join(dataDir, "auth.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open auth database: %w", err)
	}

	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	if err := runMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("run auth migrations: %w", err)
	}

	return &Service{
		db:              sqlDB,
		queries:         db.New(sqlDB),
		sessionDuration: sessionDuration,
	}, nil
}

func runMigrations(sqlDB *sql.DB) error {
	gooseProvider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, migrations.Embed)
	if err != nil {
		return fmt.Errorf("create goose provider: %w", err)
	}

	_, err = gooseProvider.Up(context.Background())
	if err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}

func (s *Service) Close() error {
	return s.db.Close()
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

// User management

func (s *Service) CreateUser(ctx context.Context, username, password string, isAdmin bool) (*db.User, error) {
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

func (s *Service) GetUserByID(ctx context.Context, id int64) (*db.User, error) {
	user, err := s.queries.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &user, nil
}

func (s *Service) GetUserByUsername(ctx context.Context, username string) (*db.User, error) {
	user, err := s.queries.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &user, nil
}

func (s *Service) ListUsers(ctx context.Context) ([]db.User, error) {
	return s.queries.ListUsers(ctx)
}

func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	return s.queries.DeleteUser(ctx, id)
}

func (s *Service) HasUsers(ctx context.Context) (bool, error) {
	count, err := s.queries.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	return count > 0, nil
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

func (s *Service) Login(ctx context.Context, username, password string) (*db.User, string, error) {
	user, err := s.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
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

// User app access

func (s *Service) GetUserApps(ctx context.Context, userID int64) ([]string, error) {
	return s.queries.GetUserApps(ctx, userID)
}

func (s *Service) AddUserApp(ctx context.Context, userID int64, appName string) error {
	return s.queries.AddUserApp(ctx, db.AddUserAppParams{
		UserID:  userID,
		AppName: appName,
	})
}

func (s *Service) RemoveUserApp(ctx context.Context, userID int64, appName string) error {
	return s.queries.RemoveUserApp(ctx, db.RemoveUserAppParams{
		UserID:  userID,
		AppName: appName,
	})
}

func (s *Service) UserHasAppAccess(ctx context.Context, userID int64, appName string) (bool, error) {
	user, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return false, err
	}

	// Admins have access to all apps
	if user.IsAdmin == 1 {
		return true, nil
	}

	apps, err := s.GetUserApps(ctx, userID)
	if err != nil {
		return false, err
	}

	for _, app := range apps {
		if app == appName {
			return true, nil
		}
	}

	return false, nil
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

func (s *Service) ValidateUserAPIKey(ctx context.Context, key string) (*db.User, error) {
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

	user, err := s.queries.GetUserByID(ctx, userKey.UserID)
	if err != nil {
		return nil, fmt.Errorf("get user for API key: %w", err)
	}

	return &user, nil
}

func (s *Service) ListUserAPIKeys(ctx context.Context, userID int64) ([]db.UserApiKey, error) {
	return s.queries.GetUserAPIKeysByUser(ctx, userID)
}

func (s *Service) DeleteUserAPIKey(ctx context.Context, id int64) error {
	return s.queries.DeleteUserAPIKey(ctx, id)
}

// TODO: Notifier and auth are completely separate domains
// Do not mix them like this
func (s *Service) GetNotifierDB(ctx context.Context, id int64) (*db.Notifier, error) {
	notifier, err := s.queries.GetNotifier(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("notifier not found")
		}
		return nil, err
	}
	return &notifier, nil
}

func (s *Service) GetNotifierByName(ctx context.Context, name string) (*db.Notifier, error) {
	notifier, err := s.queries.GetNotifierByName(ctx, name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("notifier not found")
		}
		return nil, err
	}
	return &notifier, nil
}

func (s *Service) ListNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.ListNotifiers(ctx)
}

func (s *Service) ListNotifiersByUser(ctx context.Context, userID int64) ([]db.Notifier, error) {
	return s.queries.ListNotifiersByUser(ctx, userID)
}

func (s *Service) ListEnabledNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.ListEnabledNotifiers(ctx)
}

func (s *Service) UpdateNotifier(ctx context.Context, id int64, name, notifierType, config string, enabled bool) error {
	var enabledInt int64
	if enabled {
		enabledInt = 1
	}
	return s.queries.UpdateNotifier(ctx, db.UpdateNotifierParams{
		ID:      id,
		Name:    name,
		Type:    notifierType,
		Config:  config,
		Enabled: enabledInt,
	})
}

func (s *Service) DeleteNotifier(ctx context.Context, id int64) error {
	return s.queries.DeleteNotifier(ctx, id)
}

func (s *Service) EnableNotifier(ctx context.Context, id int64) error {
	return s.queries.EnableNotifier(ctx, id)
}

func (s *Service) DisableNotifier(ctx context.Context, id int64) error {
	return s.queries.DisableNotifier(ctx, id)
}

// Notifier app mappings

func (s *Service) GetNotifierApps(ctx context.Context, notifierID int64) ([]string, error) {
	return s.queries.GetNotifierApps(ctx, notifierID)
}

func (s *Service) AddNotifierApp(ctx context.Context, notifierID int64, appName string) error {
	return s.queries.AddNotifierApp(ctx, db.AddNotifierAppParams{
		NotifierID: notifierID,
		AppName:    appName,
	})
}

func (s *Service) RemoveNotifierApp(ctx context.Context, notifierID int64, appName string) error {
	return s.queries.RemoveNotifierApp(ctx, db.RemoveNotifierAppParams{
		NotifierID: notifierID,
		AppName:    appName,
	})
}

func (s *Service) SetNotifierApps(ctx context.Context, notifierID int64, apps []string) error {
	if err := s.queries.RemoveAllNotifierApps(ctx, notifierID); err != nil {
		return err
	}
	for _, app := range apps {
		if err := s.AddNotifierApp(ctx, notifierID, app); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) GetNotifiersForApp(ctx context.Context, appName string) ([]db.Notifier, error) {
	return s.queries.GetNotifiersForApp(ctx, appName)
}

func (s *Service) GetGlobalNotifiers(ctx context.Context) ([]db.Notifier, error) {
	return s.queries.GetGlobalNotifiers(ctx)
}

// System stats methods

func (s *Service) InsertSystemStats(ctx context.Context, params db.InsertSystemStatsParams) error {
	return s.queries.InsertSystemStats(ctx, params)
}

func (s *Service) GetLatestStats(ctx context.Context, collectorName string) (*db.SystemStat, error) {
	stat, err := s.queries.GetLatestStats(ctx, collectorName)
	if err != nil {
		return nil, err
	}
	return &stat, nil
}

func (s *Service) GetLatestStatsForAllCollectors(ctx context.Context) ([]db.SystemStat, error) {
	return s.queries.GetLatestStatsForAllCollectors(ctx)
}

func (s *Service) DeleteOldStats(ctx context.Context, before time.Time) error {
	return s.queries.DeleteOldStats(ctx, before)
}

func (s *Service) GetCollectorStatsHistory(ctx context.Context, collectorName string, since time.Time) ([]db.SystemStat, error) {
	return s.queries.GetCollectorStatsHistory(ctx, db.GetCollectorStatsHistoryParams{
		CollectorName: collectorName,
		CreatedAt:     since,
	})
}

// App-collector mapping methods

func (s *Service) UpsertAppCollector(ctx context.Context, appName, collectorName string) error {
	return s.queries.UpsertAppCollector(ctx, db.UpsertAppCollectorParams{
		AppName:       appName,
		CollectorName: collectorName,
		LastSeenAt:    time.Now(),
	})
}

func (s *Service) ListAppCollectors(ctx context.Context) ([]db.AppCollector, error) {
	return s.queries.ListAppCollectors(ctx)
}

func (s *Service) GetAppsByCollector(ctx context.Context, collectorName string) ([]string, error) {
	return s.queries.GetAppsByCollector(ctx, collectorName)
}

// Adapter methods for notify.AuthService interface

// CreateNotifier creates a notifier and returns data suitable for notify service
func (s *Service) CreateNotifier(ctx context.Context, userID int64, name, notifierType, config string, enabled bool) (notify.NotifierData, error) {
	enabledInt := int64(0)
	if enabled {
		enabledInt = 1
	}

	notifier, err := s.queries.CreateNotifier(ctx, db.CreateNotifierParams{
		UserID:  userID,
		Name:    name,
		Type:    notifierType,
		Config:  config,
		Enabled: enabledInt,
	})
	if err != nil {
		return notify.NotifierData{}, err
	}

	return notify.NotifierData{
		ID:      notifier.ID,
		UserID:  notifier.UserID,
		Name:    notifier.Name,
		Type:    notifier.Type,
		Config:  notifier.Config,
		Enabled: notifier.Enabled == 1,
	}, nil
}

// GetNotifier gets a notifier and returns data suitable for notify service
func (s *Service) GetNotifier(ctx context.Context, id int64) (notify.NotifierData, error) {
	notifier, err := s.queries.GetNotifier(ctx, id)
	if err != nil {
		return notify.NotifierData{}, err
	}

	return notify.NotifierData{
		ID:      notifier.ID,
		UserID:  notifier.UserID,
		Name:    notifier.Name,
		Type:    notifier.Type,
		Config:  notifier.Config,
		Enabled: notifier.Enabled == 1,
	}, nil
}
