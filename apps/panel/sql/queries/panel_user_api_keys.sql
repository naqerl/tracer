-- User API key queries (LLM compatibility)

-- name: CreateUserAPIKey :one
INSERT INTO user_api_key (user_id, key_hash, name, created_at, key_encrypted)
VALUES (?, ?, ?, ?, ?)
RETURNING id, user_id, key_hash, name, created_at, last_used_at, key_encrypted;

-- name: GetUserAPIKeysByUser :many
SELECT id, user_id, key_hash, name, created_at, last_used_at, key_encrypted FROM user_api_key
WHERE user_id = ?
ORDER BY created_at DESC;

-- name: GetUserAPIKeyByID :one
SELECT id, user_id, key_hash, name, created_at, last_used_at, key_encrypted
FROM user_api_key
WHERE id = ?;

-- name: DeleteUserAPIKey :execrows
DELETE FROM user_api_key
WHERE id = ?;

-- name: ValidateAndUpdateUserAPIKey :one
UPDATE user_api_key
SET last_used_at = ?
WHERE key_hash = ?
RETURNING user_id;
