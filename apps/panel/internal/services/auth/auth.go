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
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
)

const (
	// DefaultBcryptCost is the production bcrypt cost. It is deliberately
	// expensive (~250ms per hash) to slow brute-force attempts.
	DefaultBcryptCost = 12

	sessionTokenSize  = 32
	apiKeySize        = 32
	maxUsernameLength = 32
	minPasswordLength = 8
	maxPasswordLength = 72
	SessionDuration   = 24 * time.Hour
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrSessionExpired     = errors.New("session expired")
	ErrUserNotFound       = errors.New("user not found")
	ErrAccessDenied       = errors.New("access denied")
)

type Service struct {
	handle     *dbmanager.DBHandle
	keySecret  []byte
	bcryptCost int
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
	if len(r.Password) < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
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

// APIKey is the service-layer view of a stored API key, shared by collector
// and user keys. CanReveal reports whether the stored ciphertext can be
// decrypted.
type APIKey struct {
	ID          int64
	Name        string
	CreatedAt   int64
	LastUsedAt  int64 // 0 when never used
	LastUsedSet bool
	CanReveal   bool
}

// CollectorAPIKey is the service-layer view of a stored collector API key.
type CollectorAPIKey struct {
	APIKey
}

// UserAPIKey is the service-layer view of a stored user API key.
type UserAPIKey struct {
	APIKey
	UserID int64
}

// New creates a Service with the given panel database handle, master key, and
// bcrypt work factor.
//
// keySecret is the master key used to encrypt API key plaintext at rest so it
// can be revealed later. Load it with LoadKeySecret. New panics if keySecret
// is not exactly keySecretSize bytes, catching misconfiguration at startup.
//
// bcryptCost is the work factor for password hashing. Production must pass
// DefaultBcryptCost; tests pass a lower cost (e.g. bcrypt.MinCost) because
// cost 12 is ~250ms per hash and would slow the suite to tens of seconds.
func New(handle *dbmanager.DBHandle, keySecret []byte, bcryptCost int) *Service {
	if len(keySecret) != keySecretSize {
		panic(fmt.Sprintf("auth.New: key secret must be %d bytes, got %d", keySecretSize, len(keySecret)))
	}
	return &Service{handle: handle, keySecret: keySecret, bcryptCost: bcryptCost}
}

// Password hashing

func (s *Service) hashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
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
func (s *Service) Register(ctx context.Context, req RegisterRequest) (domain.User, error) {
	hash, err := s.hashPassword(req.Password)
	if err != nil {
		return domain.User{}, fmt.Errorf("hash password: %w", err)
	}

	var adminInt int64
	if req.IsAdmin {
		adminInt = 1
	}

	var created db.User
	err = s.handle.Write(ctx, func(tx db.DBTX) error {
		result, err := db.New(tx).CreateUser(ctx, db.CreateUserParams{
			Username:     req.Username,
			PasswordHash: hash,
			IsAdmin:      adminInt,
			CreatedAt:    time.Now().UTC().Unix(),
		})
		if err != nil {
			return err
		}
		created = result
		return nil
	})
	if err != nil {
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}

	slog.Info("User created", "username", req.Username, "is_admin", req.IsAdmin)
	role := domain.RoleUser
	if created.IsAdmin == 1 {
		role = domain.RoleAdmin
	}
	return domain.User{ID: created.ID, Role: role}, nil
}

func (s *Service) HasAnyUsers(ctx context.Context) (bool, error) {
	queries := db.New(s.handle.Read())
	count, err := queries.CountUsers(ctx)
	if err != nil {
		return false, fmt.Errorf("check users existence: %w", err)
	}

	return count > 0, nil
}

// ResetPassword replaces the password of the user with the given username,
// promotes them to admin, and invalidates all of their existing sessions.
// Returns ErrUserNotFound if no user with that username exists.
//
// If newPassword matches the user's current password, the password write and
// session purge are skipped — sessions survive — but the user is still
// promoted to admin. This keeps repeated `-init -force` runs (e.g. on every
// container boot) from logging everyone out when the password has not
// actually changed.
func (s *Service) ResetPassword(ctx context.Context, username, newPassword string) error {
	if err := (RegisterRequest{Username: username, Password: newPassword}).Validate(); err != nil {
		return fmt.Errorf("validate reset request: %w", err)
	}

	creds, err := db.New(s.handle.Read()).FindUserCredsByUsername(ctx, username)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("could not find user by username: %w", err)
	}

	// The password is already the stored one: a reset has nothing to do, and
	// purging sessions — the whole point of a real reset — is the bug this
	// avoids. Still assert admin so the "ensure the admin user has this
	// password" contract holds, via a cheap idempotent UPDATE instead of a
	// re-hash + session purge.
	err = checkPassword(newPassword, creds.PasswordHash)
	if err == nil {
		if err := s.handle.Write(ctx, func(tx db.DBTX) error {
			return db.New(tx).SetUserAdmin(ctx, creds.ID)
		}); err != nil {
			return fmt.Errorf("could not reset password: %w", err)
		}
		slog.Info("Password unchanged, reset is a no-op", "username", username)
		return nil
	}
	if !errors.Is(err, ErrInvalidCredentials) {
		// Unexpected check failure (e.g. a corrupt stored hash): log it and
		// let the reset below re-hash, which repairs the stored hash.
		slog.Warn("Password check failed, proceeding with reset", "username", username, "error", err)
	}

	hash, err := s.hashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("could not hash password: %w", err)
	}

	err = s.handle.Write(ctx, func(tx db.DBTX) error {
		q := db.New(tx)
		rows, err := q.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
			ID:           creds.ID,
			PasswordHash: hash,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		if rows == 0 {
			// User was deleted between the read and the write.
			return ErrUserNotFound
		}
		if err := q.SetUserAdmin(ctx, creds.ID); err != nil {
			return err
		}
		return q.DeleteSessionsByUser(ctx, creds.ID)
	})
	if err != nil {
		return fmt.Errorf("could not reset password: %w", err)
	}

	slog.Info("Password reset", "username", username)
	return nil
}

// Session management

func (s *Service) CreateSession(ctx context.Context, userID int64) (string, error) {
	token, err := generateToken(sessionTokenSize)
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}

	expiresAt := time.Now().Add(SessionDuration).Unix()
	err = s.handle.Write(ctx, func(tx db.DBTX) error {
		return db.New(tx).CreateSession(ctx, db.CreateSessionParams{
			ID:        token,
			UserID:    userID,
			ExpiresAt: expiresAt,
			CreatedAt: time.Now().UTC().Unix(),
		})
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	return token, nil
}

// ValidateSession returns the session's user ID. Expired sessions are deleted
// and reported as ErrSessionExpired. When at least half of the session's
// lifetime has elapsed, the deadline is extended by SessionDuration and
// refresh is true so the caller can re-issue the cookie; sessions that still
// have more than half their lifetime keep their deadline untouched.
func (s *Service) ValidateSession(ctx context.Context, token string) (userID int64, refresh bool, err error) {
	session, err := db.New(s.handle.Read()).GetSession(ctx, token)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, ErrSessionExpired
	}
	if err != nil {
		return 0, false, fmt.Errorf("could not get data from db: %w", err)
	}

	now := time.Now().Unix()
	if now > session.ExpiresAt {
		err = s.handle.Write(ctx, func(tx db.DBTX) error {
			return db.New(tx).DeleteSession(ctx, token)
		})
		if err != nil {
			return 0, false, fmt.Errorf("could not delete session: %w", err)
		}
		return 0, false, ErrSessionExpired
	}

	if session.ExpiresAt-now <= int64(SessionDuration.Seconds())/2 {
		err = s.handle.Write(ctx, func(tx db.DBTX) error {
			return db.New(tx).RefreshSession(ctx, db.RefreshSessionParams{
				ExpiresAt: now + int64(SessionDuration.Seconds()),
				ID:        token,
			})
		})
		if err != nil {
			return 0, false, fmt.Errorf("could not refresh session: %w", err)
		}
		refresh = true
	}

	return session.UserID, refresh, nil
}

func (s *Service) DeleteSession(ctx context.Context, token string) error {
	return s.handle.Write(ctx, func(tx db.DBTX) error {
		return db.New(tx).DeleteSession(ctx, token)
	})
}

func (s *Service) CleanupExpiredSessions(ctx context.Context) error {
	return s.handle.Write(ctx, func(tx db.DBTX) error {
		return db.New(tx).DeleteExpiredSessions(ctx, time.Now().Unix())
	})
}

// Login

func (s *Service) Login(ctx context.Context, username, password string) (string, error) {
	queries := db.New(s.handle.Read())
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

// GenerateCollectorAPIKey creates a new collector API key.
func (s *Service) GenerateCollectorAPIKey(ctx context.Context, actor domain.Actor, req GenerateCollectorAPIKeyRequest) (string, error) {
	if err := authorizeAdminOrSystem(actor); err != nil {
		return "", err
	}
	key, err := generateToken(apiKeySize)
	if err != nil {
		return "", fmt.Errorf("generate collector api key: %w", err)
	}

	encrypted, err := encryptKey(s.keySecret, key)
	if err != nil {
		return "", fmt.Errorf("encrypt collector api key: %w", err)
	}

	err = s.handle.Write(ctx, func(tx db.DBTX) error {
		_, err := db.New(tx).CreateCollectorAPIKey(ctx, db.CreateCollectorAPIKeyParams{
			Name:         req.Name,
			KeyHash:      hashAPIKey(key),
			KeyEncrypted: encrypted,
			CreatedAt:    time.Now().UTC().Unix(),
		})
		return err
	})
	if err != nil {
		return "", fmt.Errorf("create collector api key: %w", err)
	}

	slog.Info("Collector API key created", "name", req.Name)
	return key, nil
}

// ListCollectorAPIKeys returns all collector API keys.
func (s *Service) ListCollectorAPIKeys(ctx context.Context, actor domain.Actor) ([]CollectorAPIKey, error) {
	if err := authorizeAdminOrSystem(actor); err != nil {
		return nil, err
	}
	rows, err := db.New(s.handle.Read()).GetCollectorAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	keys := make([]CollectorAPIKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, collectorAPIKeyFromRow(row))
	}
	return keys, nil
}

// GetCollectorAPIKeyByID returns a collector API key by id.
func (s *Service) GetCollectorAPIKeyByID(ctx context.Context, actor domain.Actor, id int64) (CollectorAPIKey, error) {
	if err := authorizeAdminOrSystem(actor); err != nil {
		return CollectorAPIKey{}, err
	}
	row, err := db.New(s.handle.Read()).GetCollectorAPIKeyByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return CollectorAPIKey{}, err
	}
	if err != nil {
		return CollectorAPIKey{}, fmt.Errorf("could not get collector API key: %w", err)
	}
	return collectorAPIKeyFromRow(row), nil
}

// RevealCollectorAPIKey decrypts and returns the plaintext of the collector
// API key with the given id. Returns ErrKeyNotRevealable when the key has no
// stored ciphertext and ErrKeyDecryptFailed when decryption fails.
func (s *Service) RevealCollectorAPIKey(ctx context.Context, actor domain.Actor, id int64) (string, error) {
	if err := authorizeAdminOrSystem(actor); err != nil {
		return "", err
	}
	row, err := db.New(s.handle.Read()).GetCollectorAPIKeyByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("could not get collector API key: %w", err)
	}
	return decryptKey(s.keySecret, row.KeyEncrypted)
}

// DeleteCollectorAPIKey deletes a collector API key by id.
func (s *Service) DeleteCollectorAPIKey(ctx context.Context, actor domain.Actor, id int64) error {
	if err := authorizeAdminOrSystem(actor); err != nil {
		return err
	}
	return s.handle.Write(ctx, func(tx db.DBTX) error {
		rows, err := db.New(tx).DeleteCollectorAPIKey(ctx, id)
		if err != nil {
			return err
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

func (s *Service) ValidateCollectorAPIKey(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, nil
	}

	var rows int64
	err := s.handle.Write(ctx, func(tx db.DBTX) error {
		result, err := db.New(tx).ValidateAndUpdateCollectorAPIKey(ctx, db.ValidateAndUpdateCollectorAPIKeyParams{
			LastUsedAt: sql.NullInt64{Int64: time.Now().UTC().Unix(), Valid: true},
			KeyHash:    hashAPIKey(key),
		})
		if err != nil {
			return err
		}
		rows = result
		return nil
	})
	if err != nil {
		return false, err
	}
	if rows == 0 {
		return false, nil
	}
	return true, nil
}

// User API Key management (LLM compatibility)

func (s *Service) ValidateUserAPIKey(ctx context.Context, key string) (int64, error) {
	var userID int64
	err := s.handle.Write(ctx, func(tx db.DBTX) error {
		result, err := db.New(tx).ValidateAndUpdateUserAPIKey(ctx, db.ValidateAndUpdateUserAPIKeyParams{
			LastUsedAt: sql.NullInt64{Int64: time.Now().UTC().Unix(), Valid: true},
			KeyHash:    hashAPIKey(key),
		})
		if err != nil {
			return err
		}
		userID = result
		return nil
	})

	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrInvalidCredentials
	}

	return userID, err
}

// ListUserAPIKeys returns the API keys owned by the given user.
func (s *Service) ListUserAPIKeys(ctx context.Context, actor domain.Actor, userID int64) ([]UserAPIKey, error) {
	if err := authorizeOwnerOrAdmin(actor, userID); err != nil {
		return nil, err
	}
	rows, err := db.New(s.handle.Read()).GetUserAPIKeysByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	keys := make([]UserAPIKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, userAPIKeyFromRow(row))
	}
	return keys, nil
}

// GetUserAPIKeyByID returns a user API key by id. The lookup happens first
// because ownership is only known from the row; handlers map ErrAccessDenied
// on the read path to 404 so key existence is not revealed to non-owners.
func (s *Service) GetUserAPIKeyByID(ctx context.Context, actor domain.Actor, id int64) (UserAPIKey, error) {
	row, err := db.New(s.handle.Read()).GetUserAPIKeyByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return UserAPIKey{}, err
	}
	if err != nil {
		return UserAPIKey{}, fmt.Errorf("could not get user API key: %w", err)
	}
	if err := authorizeOwnerOrAdmin(actor, row.UserID); err != nil {
		return UserAPIKey{}, err
	}
	return userAPIKeyFromRow(row), nil
}

// RevealUserAPIKey decrypts and returns the plaintext of the user API key
// with the given id. Returns ErrKeyNotRevealable when the key has no stored
// ciphertext and ErrKeyDecryptFailed when decryption fails.
func (s *Service) RevealUserAPIKey(ctx context.Context, actor domain.Actor, id int64) (string, error) {
	row, err := db.New(s.handle.Read()).GetUserAPIKeyByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("could not get user API key: %w", err)
	}
	if err := authorizeOwnerOrAdmin(actor, row.UserID); err != nil {
		return "", err
	}
	return decryptKey(s.keySecret, row.KeyEncrypted)
}

// authorizeAdminOrSystem permits the system actor and admin users only.
func authorizeAdminOrSystem(actor domain.Actor) error {
	switch a := actor.(type) {
	case domain.System:
		return nil
	case domain.User:
		if a.Role == domain.RoleAdmin {
			return nil
		}
		return ErrAccessDenied
	case domain.Unauthenticated:
		return ErrAccessDenied
	default:
		return domain.ErrUnexpectedActor
	}
}

// authorizeOwnerOrAdmin permits the system actor, admin users, and the user
// that owns the resource (ownerID).
func authorizeOwnerOrAdmin(actor domain.Actor, ownerID int64) error {
	switch a := actor.(type) {
	case domain.System:
		return nil
	case domain.User:
		if a.Role == domain.RoleAdmin || a.ID == ownerID {
			return nil
		}
		return ErrAccessDenied
	case domain.Unauthenticated:
		return ErrAccessDenied
	default:
		return domain.ErrUnexpectedActor
	}
}

// DeleteUserAPIKey deletes a user API key by id.
func (s *Service) DeleteUserAPIKey(ctx context.Context, actor domain.Actor, id int64) error {
	key, err := db.New(s.handle.Read()).GetUserAPIKeyByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err != nil {
		return fmt.Errorf("could not get user API key: %w", err)
	}
	if err := authorizeOwnerOrAdmin(actor, key.UserID); err != nil {
		return err
	}
	return s.handle.Write(ctx, func(tx db.DBTX) error {
		rows, err := db.New(tx).DeleteUserAPIKey(ctx, id)
		if err != nil {
			return err
		}
		if rows == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

// GenerateUserAPIKey creates a new API key for the given user.
func (s *Service) GenerateUserAPIKey(ctx context.Context, actor domain.Actor, req GenerateUserAPIKeyRequest) (string, error) {
	if err := authorizeOwnerOrAdmin(actor, req.UserID); err != nil {
		return "", err
	}

	key, err := generateToken(apiKeySize)
	if err != nil {
		return "", fmt.Errorf("generate user API key: %w", err)
	}

	encrypted, err := encryptKey(s.keySecret, key)
	if err != nil {
		return "", fmt.Errorf("encrypt user API key: %w", err)
	}

	err = s.handle.Write(ctx, func(tx db.DBTX) error {
		_, err := db.New(tx).CreateUserAPIKey(ctx, db.CreateUserAPIKeyParams{
			UserID:       req.UserID,
			KeyHash:      hashAPIKey(key),
			Name:         req.Name,
			CreatedAt:    time.Now().UTC().Unix(),
			KeyEncrypted: encrypted,
		})
		return err
	})
	if err != nil {
		return "", fmt.Errorf("create user API key: %w", err)
	}

	slog.Info("User API key created", "user_id", req.UserID, "name", req.Name)
	return key, nil
}

// collectorAPIKeyFromRow converts a stored collector API key row into the
// service-layer view.
func collectorAPIKeyFromRow(row db.CollectorApiKey) CollectorAPIKey {
	return CollectorAPIKey{
		APIKey: apiKeyFromRow(row.ID, row.Name, row.CreatedAt, row.LastUsedAt, row.KeyEncrypted),
	}
}

// userAPIKeyFromRow converts a stored user API key row into the service-layer
// view.
func userAPIKeyFromRow(row db.UserApiKey) UserAPIKey {
	return UserAPIKey{
		APIKey: apiKeyFromRow(row.ID, row.Name, row.CreatedAt, row.LastUsedAt, row.KeyEncrypted),
		UserID: row.UserID,
	}
}

// apiKeyFromRow converts the shared API key fields of a stored row into the
// service-layer view.
func apiKeyFromRow(id int64, name string, createdAt int64, lastUsedAt sql.NullInt64, keyEncrypted string) APIKey {
	return APIKey{
		ID:          id,
		Name:        name,
		CreatedAt:   createdAt,
		LastUsedAt:  lastUsedAt.Int64,
		LastUsedSet: lastUsedAt.Valid,
		CanReveal:   keyEncrypted != "",
	}
}
