-- User queries

-- name: CreateUser :one
INSERT INTO user (username, password_hash, is_admin)
VALUES (?, ?, ?)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM user
WHERE id = ?;

-- name: GetUserByUsername :one
SELECT * FROM user
WHERE username = ?;

-- name: ListUsers :many
SELECT * FROM user
ORDER BY created_at;

-- name: DeleteUser :exec
DELETE FROM user
WHERE id = ?;

-- name: CountUsers :one
SELECT COUNT(*) FROM user;

-- Session queries

-- name: CreateSession :exec
INSERT INTO session (id, user_id, expires_at)
VALUES (?, ?, ?);

-- name: GetSession :one
SELECT * FROM session
WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM session
WHERE id = ?;

-- name: DeleteUserSessions :exec
DELETE FROM session
WHERE user_id = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM session
WHERE expires_at < ?;

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

-- Config queries

-- name: GetConfig :one
SELECT value FROM config
WHERE key = ?;

-- name: SetConfig :exec
INSERT INTO config (key, value)
VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

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

-- System stats queries

-- name: InsertSystemStats :exec
INSERT INTO system_stats (
    collector_name, cpu_usage_percent, mem_total_mb, mem_used_mb,
    mem_available_mb, disk_total_gb, disk_used_gb, disk_avail_gb, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetLatestStats :one
SELECT * FROM system_stats
WHERE collector_name = ?
ORDER BY created_at DESC
LIMIT 1;

-- name: GetLatestStatsForAllCollectors :many
SELECT s1.* FROM system_stats s1
INNER JOIN (
    SELECT collector_name, MAX(created_at) as max_created
    FROM system_stats
    GROUP BY collector_name
) s2 ON s1.collector_name = s2.collector_name AND s1.created_at = s2.max_created;

-- name: DeleteOldStats :exec
DELETE FROM system_stats
WHERE created_at < ?;

-- name: GetCollectorStatsHistory :many
SELECT * FROM system_stats
WHERE collector_name = ? AND created_at >= ?
ORDER BY created_at ASC;

-- App-collector mapping queries

-- name: UpsertAppCollector :exec
INSERT INTO app_collectors (app_name, collector_name, last_seen_at)
VALUES (?, ?, ?)
ON CONFLICT(app_name, collector_name) 
DO UPDATE SET last_seen_at = excluded.last_seen_at;

-- name: ListAppCollectors :many
SELECT * FROM app_collectors
ORDER BY app_name, collector_name;

-- name: GetAppsByCollector :many
SELECT app_name FROM app_collectors
WHERE collector_name = ?
ORDER BY app_name;

-- name: GetCollectorsByApp :many
SELECT collector_name FROM app_collectors
WHERE app_name = ?
ORDER BY collector_name;
