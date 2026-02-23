-- User API key queries (LLM compatibility)

-- name: CreateUserAPIKey :one
INSERT INTO user_api_key (user_id, api_key, name)
VALUES (?, ?, ?)
RETURNING *;

-- name: GetUserAPIKey :one
SELECT * FROM user_api_key
WHERE api_key = ?;

-- name: GetUserAPIKeysByUser :many
SELECT * FROM user_api_key
WHERE user_id = ?
ORDER BY created_at DESC;

-- name: DeleteUserAPIKey :exec
DELETE FROM user_api_key
WHERE id = ?;

-- name: UpdateUserAPIKeyLastUsed :exec
UPDATE user_api_key
SET last_used_at = ?
WHERE api_key = ?;
