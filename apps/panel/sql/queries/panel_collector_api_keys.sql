-- Collector API keys

-- name: CreateCollectorAPIKey :one
INSERT INTO collector_api_key (name, key_hash, key_encrypted, created_at)
VALUES (?, ?, ?, ?)
RETURNING id, name, key_hash, key_encrypted, created_at, last_used_at;

-- name: GetCollectorAPIKeyByID :one
SELECT id, name, key_hash, key_encrypted, created_at, last_used_at FROM collector_api_key
WHERE id = ?;

-- name: GetCollectorAPIKeys :many
SELECT id, name, key_hash, key_encrypted, created_at, last_used_at FROM collector_api_key
ORDER BY created_at DESC, name DESC;

-- name: DeleteCollectorAPIKey :execrows
DELETE FROM collector_api_key
WHERE id = ?;

-- name: ValidateAndUpdateCollectorAPIKey :execrows
UPDATE collector_api_key
SET last_used_at = ?
WHERE key_hash = ?;
