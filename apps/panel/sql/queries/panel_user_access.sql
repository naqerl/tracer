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
