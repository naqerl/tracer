-- name: GetCollectorConfig :one
SELECT config_json FROM collector_config WHERE id = 1;

-- name: UpsertCollectorConfig :exec
INSERT INTO collector_config (id, config_json, updated_at) VALUES (1, @config_json, @updated_at)
ON CONFLICT(id) DO UPDATE SET config_json = excluded.config_json, updated_at = excluded.updated_at;
