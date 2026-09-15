-- User access policy queries

-- name: FindUserAccessPolicy :many
SELECT collector_name, app_name FROM access_policy
WHERE user_id = ?
ORDER BY collector_name, app_name;

-- name: GrantAppAccess :exec
INSERT OR IGNORE INTO access_policy (user_id, collector_name, app_name)
VALUES (?, ?, ?);

-- name: RevokeAppAccess :exec
DELETE FROM access_policy
WHERE user_id = ? AND collector_name = ? AND app_name = ?;

-- name: RenameAppAccess :exec
UPDATE access_policy
SET app_name = sqlc.arg(new_name)
WHERE collector_name = sqlc.arg(collector_name) AND app_name = sqlc.arg(old_name);
