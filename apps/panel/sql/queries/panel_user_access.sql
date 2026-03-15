-- User app access queries

-- name: GetUserApps :many
SELECT collector_name, app_name FROM user_app
WHERE user_id = ?
ORDER BY collector_name, app_name;

-- name: AddUserApp :exec
INSERT OR IGNORE INTO user_app (user_id, collector_name, app_name)
VALUES (?, ?, ?);

-- name: RemoveUserApp :exec
DELETE FROM user_app
WHERE user_id = ? AND collector_name = ? AND app_name = ?;
