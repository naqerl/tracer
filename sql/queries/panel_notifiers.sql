-- Notifier queries

-- name: CreateNotifier :one
INSERT INTO notifier (user_id, name, type, config, enabled)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetNotifier :one
SELECT * FROM notifier
WHERE id = ?;

-- name: GetNotifierByName :one
SELECT * FROM notifier
WHERE name = ?;

-- name: ListNotifiers :many
SELECT * FROM notifier
ORDER BY created_at;

-- name: ListNotifiersByUser :many
SELECT * FROM notifier
WHERE user_id = ?
ORDER BY created_at;

-- name: ListEnabledNotifiers :many
SELECT * FROM notifier
WHERE enabled = 1
ORDER BY created_at;

-- name: UpdateNotifier :exec
UPDATE notifier
SET name = ?, type = ?, config = ?, enabled = ?
WHERE id = ?;

-- name: DeleteNotifier :exec
DELETE FROM notifier
WHERE id = ?;

-- name: EnableNotifier :exec
UPDATE notifier SET enabled = 1 WHERE id = ?;

-- name: DisableNotifier :exec
UPDATE notifier SET enabled = 0 WHERE id = ?;

-- Notifier app mapping queries

-- name: GetNotifierApps :many
SELECT app_name FROM notifier_app
WHERE notifier_id = ?
ORDER BY app_name;

-- name: AddNotifierApp :exec
INSERT OR IGNORE INTO notifier_app (notifier_id, app_name)
VALUES (?, ?);

-- name: RemoveNotifierApp :exec
DELETE FROM notifier_app
WHERE notifier_id = ? AND app_name = ?;

-- name: RemoveAllNotifierApps :exec
DELETE FROM notifier_app
WHERE notifier_id = ?;

-- name: GetNotifiersForApp :many
SELECT n.* FROM notifier n
JOIN notifier_app na ON n.id = na.notifier_id
WHERE na.app_name = ? AND n.enabled = 1;

-- name: GetGlobalNotifiers :many
SELECT n.* FROM notifier n
JOIN notifier_app na ON n.id = na.notifier_id
WHERE na.app_name = '*' AND n.enabled = 1;
