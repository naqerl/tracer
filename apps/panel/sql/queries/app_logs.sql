-- name: InsertLog :one
INSERT INTO logs (trace_id, level, message, context, group_id, created_at)
VALUES (?, ?, ?, ?, ?, ?)
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

-- name: CountLogGroups :one
-- Count total log groups
SELECT COUNT(*) as total
FROM log_groups;

-- name: CountLogGroupsByLevel :one
-- Count log groups filtered by level
SELECT COUNT(*) as total
FROM log_groups
WHERE LOWER(level) = LOWER(?);

-- name: ListLogGroups :many
-- List all log groups with their counts and latest timestamp
SELECT
    lg.id,
    lg.message,
    lg.level,
    COUNT(l.id) as count,
    (SELECT created_at FROM logs WHERE group_id = lg.id ORDER BY created_at DESC LIMIT 1) as latest_at,
    lg.created_at as created_at
FROM log_groups lg
INNER JOIN logs l ON l.group_id = lg.id
WHERE lg.level like ?
  AND l.created_at >= ?
GROUP BY lg.id, lg.message, lg.level, lg.created_at
ORDER BY count DESC, latest_at DESC
LIMIT ? OFFSET ?;

-- name: GetLogsByGroupID :many
-- Get all individual logs for a specific group
SELECT id, level, context, created_at
FROM logs
WHERE group_id = ?
ORDER BY created_at DESC
LIMIT ? OFFSET ?;

-- name: CountLogsByGroupID :one
-- Count total logs in a group
SELECT COUNT(*) as total
FROM logs
WHERE group_id = ?;

-- name: ListRecentLogs :many
SELECT id, trace_id, level, message, context, group_id, created_at FROM logs
ORDER BY created_at DESC
LIMIT ?;

-- name: ListLogsByLevel :many
SELECT id, trace_id, level, message, context, group_id, created_at FROM logs
WHERE level = ?
ORDER BY created_at DESC
LIMIT ?;

-- name: FindLogsByTraceID :many
SELECT id, trace_id, level, message, context, group_id, created_at FROM logs
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
SELECT id, trace_id, level, message, context, group_id, created_at FROM logs
WHERE id = ?;

-- name: SearchLogs :many
SELECT id, trace_id, level, message, context, group_id, created_at FROM logs
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

-- name: GetLogVolumeTimeSeries :many
-- Get log volume over time bucketed by intervals and grouped by level
SELECT
    CASE
        WHEN sqlc.arg(granularity) = '1min' THEN strftime('%Y-%m-%d %H:%M:00', substr(created_at, 1, 19))
        WHEN sqlc.arg(granularity) = '5min' THEN
            strftime('%Y-%m-%d %H:', substr(created_at, 1, 19)) ||
            printf('%02d', (CAST(strftime('%M', substr(created_at, 1, 19)) AS INTEGER) / 5) * 5) || ':00'
        WHEN sqlc.arg(granularity) = '15min' THEN
            strftime('%Y-%m-%d %H:', substr(created_at, 1, 19)) ||
            printf('%02d', (CAST(strftime('%M', substr(created_at, 1, 19)) AS INTEGER) / 15) * 15) || ':00'
        WHEN sqlc.arg(granularity) = '1hour' THEN strftime('%Y-%m-%d %H:00:00', substr(created_at, 1, 19))
    END as time_bucket,
    LOWER(level) as level,
    COUNT(*) as count
FROM logs
WHERE created_at >= sqlc.arg(since)
GROUP BY time_bucket, LOWER(level)
ORDER BY time_bucket ASC, level;
