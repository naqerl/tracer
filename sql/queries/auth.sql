-- User queries

-- name: CreateUser :one
INSERT INTO user (username, password_hash, is_admin)
VALUES (?, ?, ?)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM user
WHERE id = ?;

-- name: GetUserByUsername :one
SELECT * FROM user
WHERE username = ?;

-- name: ListUsers :many
SELECT * FROM user
ORDER BY created_at;

-- name: DeleteUser :exec
DELETE FROM user
WHERE id = ?;

-- name: CountUsers :one
SELECT COUNT(*) FROM user;

-- Session queries

-- name: CreateSession :exec
INSERT INTO session (id, user_id, expires_at)
VALUES (?, ?, ?);

-- name: GetSession :one
SELECT * FROM session
WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM session
WHERE id = ?;

-- name: DeleteUserSessions :exec
DELETE FROM session
WHERE user_id = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM session
WHERE expires_at < ?;

-- User app access queries

-- name: GetUserApps :many
SELECT app_name FROM user_app
WHERE user_id = ?
ORDER BY app_name;

-- name: AddUserApp :exec
INSERT OR IGNORE INTO user_app (user_id, app_name)
VALUES (?, ?);

-- name: RemoveUserApp :exec
DELETE FROM user_app
WHERE user_id = ? AND app_name = ?;

-- name: RemoveAllUserApps :exec
DELETE FROM user_app
WHERE user_id = ?;

-- Config queries

-- name: GetConfig :one
SELECT value FROM config
WHERE key = ?;

-- name: SetConfig :exec
INSERT INTO config (key, value)
VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;
