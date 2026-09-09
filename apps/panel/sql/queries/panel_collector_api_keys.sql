-- Collector API keys

-- name: CreateCollectorAPIKey :one
INSERT INTO collector_api_key (name, key_hash, key_encrypted, created_at, user_id)
VALUES (?, ?, ?, ?, ?)
RETURNING id, name, key_hash, key_encrypted, created_at, last_used_at, user_id;

-- name: GetCollectorAPIKeyByID :one
SELECT id, name, key_hash, key_encrypted, created_at, last_used_at, user_id FROM collector_api_key
WHERE id = ?;

-- name: GetCollectorAPIKeys :many
SELECT id, name, key_hash, key_encrypted, created_at, last_used_at, user_id FROM collector_api_key
ORDER BY created_at DESC, name DESC;

-- name: DeleteCollectorAPIKey :execrows
DELETE FROM collector_api_key
WHERE id = ?;

-- name: GetUserByCollectorAPIKey :one
SELECT u.id, u.username, u.is_admin, u.created_at FROM user u
JOIN collector_api_key k ON k.user_id = u.id
WHERE k.id = ?;

-- name: ValidateAndUpdateCollectorAPIKey :one
SELECT id, name, user_id FROM collector_api_key
WHERE key_hash = ?;

-- name: TouchCollectorAPIKey :execrows
UPDATE collector_api_key
SET last_used_at = ?
WHERE key_hash = ?;
