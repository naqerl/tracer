-- Collector API keys

-- name: CreateCollectorAPIKey :one
INSERT INTO collector_api_key (name, key_hash, key_suffix, key_encrypted, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING name, key_hash, key_suffix, created_at, last_used_at, key_encrypted;

-- name: GetCollectorAPIKeyByName :one
SELECT name, key_hash, key_suffix, created_at, last_used_at, key_encrypted FROM collector_api_key
WHERE name = ?;

-- name: GetCollectorAPIKeys :many
SELECT name, key_suffix, created_at, last_used_at FROM collector_api_key
ORDER BY created_at DESC, name DESC;

-- name: DeleteCollectorAPIKey :exec
DELETE FROM collector_api_key
WHERE name = ?;

-- name: ValidateAndUpdateCollectorAPIKey :execrows
UPDATE collector_api_key
SET last_used_at = ?
WHERE key_hash = ?;
