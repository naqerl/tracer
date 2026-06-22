-- User API key queries (LLM compatibility)

-- name: CreateUserAPIKey :one
INSERT INTO user_api_key (user_id, key_hash, key_suffix, name, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetUserAPIKeysByUser :many
SELECT id, user_id, key_suffix, name, last_used_at FROM user_api_key
WHERE user_id = ?
ORDER BY created_at DESC;

-- name: DeleteUserAPIKey :exec
DELETE FROM user_api_key
WHERE id = ?;

-- name: ValidateAndUpdateUserAPIKey :one
UPDATE user_api_key
SET last_used_at = ?
WHERE key_hash = ?
RETURNING user_id;
