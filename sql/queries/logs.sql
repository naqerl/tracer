-- name: InsertLog :one
INSERT INTO logs (trace_id, level, message, source, context, raw, group_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetOrCreateLogGroup :one
-- Get existing log group by message and level, or create if doesn't exist
INSERT INTO log_groups (message, level)
VALUES (?, ?)
ON CONFLICT(message, level) DO UPDATE SET message = message
RETURNING *;

-- name: GetLogGroupByID :one
-- Lookup log group by ID
SELECT * FROM log_groups
WHERE id = ?;

-- name: ListLogGroups :many
-- List all log groups with their counts and latest timestamp
SELECT
    lg.id,
    lg.message,
    lg.level,
    COUNT(l.id) as count,
    MAX(l.created_at) as latest_at,
    lg.created_at as group_created_at
FROM log_groups lg
LEFT JOIN logs l ON l.group_id = lg.id
GROUP BY lg.id, lg.message, lg.level, lg.created_at
ORDER BY count DESC, latest_at DESC
LIMIT ?;

-- name: ListLogGroupsByLevel :many
-- List log groups filtered by level with counts
SELECT
    lg.id,
    lg.message,
    lg.level,
    COUNT(l.id) as count,
    MAX(l.created_at) as latest_at,
    lg.created_at as group_created_at
FROM log_groups lg
INNER JOIN logs l ON l.group_id = lg.id
WHERE LOWER(lg.level) = LOWER(?)
GROUP BY lg.id, lg.message, lg.level, lg.created_at
ORDER BY count DESC, latest_at DESC
LIMIT ?;

-- name: GetLogsByGroupID :many
-- Get all individual logs for a specific group
SELECT id, level, context, created_at
FROM logs
WHERE group_id = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: CountLogsByGroupID :one
-- Count total logs in a group
SELECT COUNT(*) as total
FROM logs
WHERE group_id = ?;

-- name: ListRecentLogs :many
SELECT * FROM logs
ORDER BY created_at DESC
LIMIT ?;

-- name: ListLogsByLevel :many
SELECT * FROM logs
WHERE level = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: FindLogsByTraceID :many
SELECT * FROM logs
WHERE trace_id = ?
ORDER BY created_at;

-- name: CountByLevel :many
SELECT level, COUNT(*) as count
FROM logs
WHERE created_at >= ?
GROUP BY level;

-- name: DeleteOldLogs :exec
DELETE FROM logs
WHERE created_at < ?;

-- name: GetLogByID :one
SELECT * FROM logs
WHERE id = ?;

-- name: SearchLogs :many
SELECT * FROM logs
WHERE message LIKE ?
ORDER BY created_at DESC
LIMIT ?;

-- name: GetPercentiles :one
SELECT 
    COUNT(*) as total,
    (SELECT COUNT(*) FROM logs l2 WHERE LOWER(l2.level) = 'error' AND l2.created_at >= ?1) as errors,
    (SELECT COUNT(*) FROM logs l3 WHERE LOWER(l3.level) = 'warn' AND l3.created_at >= ?1) as warnings,
    (SELECT COUNT(*) FROM logs l4 WHERE LOWER(l4.level) = 'info' AND l4.created_at >= ?1) as infos
FROM logs l1
WHERE l1.created_at >= ?1;

