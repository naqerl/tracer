-- Notifier queries

-- name: CreateNotifier :one
INSERT INTO notifier (user_id, name, type, config, enabled, created_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetNotifier :one
SELECT * FROM notifier
WHERE id = ?;

-- name: ListAllNotifiers :many
SELECT * FROM notifier
ORDER BY created_at;

-- name: ListNotifiersByUser :many
SELECT * FROM notifier
WHERE user_id = ?
ORDER BY created_at;

-- name: DeleteNotifier :exec
DELETE FROM notifier
WHERE id = ?;

-- name: EnableNotifier :exec
UPDATE notifier SET enabled = 1 WHERE id = ?;

-- name: DisableNotifier :exec
UPDATE notifier SET enabled = 0 WHERE id = ?;

-- Notifier subscription mapping queries

-- name: GetNotifierSubscriptions :many
-- Returns all subscriptions (with collector) for a notifier
SELECT app_name, collector_name FROM notifier_subscription
WHERE notifier_id = ?;

-- name: AddNotifierSubscription :exec
INSERT INTO notifier_subscription (notifier_id, app_name, collector_name)
VALUES (?, ?, ?);

-- name: RemoveAllNotifierSubscriptions :exec
DELETE FROM notifier_subscription
WHERE notifier_id = ?;

-- name: RenameNotifierSubscription :exec
UPDATE notifier_subscription
SET app_name = sqlc.arg(new_name)
WHERE collector_name = sqlc.arg(collector_name) AND app_name = sqlc.arg(old_name);

-- name: GetMatchingNotifiers :many
-- Returns all enabled notifiers that match the given collector and app.
-- Handles four levels of matching:
-- 1. Exact match: collector + app
-- 2. Any collector for this app (collector = "*")
-- 3. All apps from this specific collector (app = "*", collector = specific)
-- 4. All apps from any collector (app = "*", collector = "*")
SELECT DISTINCT n.* FROM notifier n
JOIN notifier_subscription ns ON n.id = ns.notifier_id
WHERE n.enabled = 1 AND (
    (ns.collector_name = sqlc.arg(collector_name) AND ns.app_name = sqlc.arg(app_name)) OR
    (ns.collector_name = '*' AND ns.app_name = sqlc.arg(app_name)) OR
    (ns.collector_name = sqlc.arg(collector_name) AND ns.app_name = '*') OR
    (ns.collector_name = '*' AND ns.app_name = '*')
);
