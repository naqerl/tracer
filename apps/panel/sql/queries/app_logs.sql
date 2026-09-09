-- name: InsertLog :one
INSERT INTO log (trace_id, level, message, context, group_id, created_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id, trace_id, level, message, context, group_id, created_at;

-- name: IncrementLogGroupCounter :exec
-- Update last_log_at for a group
UPDATE log_group
SET last_log_at = ?
WHERE id = ?;

-- name: GetOrCreateLogGroup :one
-- Get existing log group by message and level, or create if doesn't exist
INSERT INTO log_group (message, level, created_at, last_log_at)
VALUES (?, ?, ?, 0)
ON CONFLICT(message, level) DO UPDATE SET message = message
RETURNING id, message, level, created_at, last_log_at;

-- name: GetLogGroupByID :one
SELECT
    message,
    level,
    created_at,
    CAST(COALESCE((SELECT MAX(created_at) FROM log WHERE log.group_id = ?1), 0) AS INTEGER) as last_log_at,
    (SELECT COUNT(*) FROM log WHERE log.group_id = ?1) as log_count
FROM log_group
WHERE id = ?1;

-- name: ListLogGroups :many
WITH group_stats AS (
    SELECT 
        l.group_id,
        COUNT(*) as count,
        MAX(l.created_at) as last_log_at
    FROM log l
    WHERE l.created_at >= ?1
    GROUP BY l.group_id
)
SELECT
    lg.id,
    lg.message,
    lg.level,
    gs.count,
    CAST(gs.last_log_at AS INTEGER) as last_log_at,
    lg.created_at
FROM log_group lg
INNER JOIN group_stats gs ON gs.group_id = lg.id
WHERE lg.level LIKE ?2
ORDER BY gs.count DESC, gs.last_log_at DESC
LIMIT ?3 OFFSET ?4;

-- name: GetLogsByGroupID :many
-- Get all individual logs for a specific group
SELECT id, level, context, created_at
FROM log
WHERE group_id = ?
  AND created_at >= ?
ORDER BY created_at DESC
LIMIT ? OFFSET ?;

-- name: CountLogsByGroupID :one
-- Count total logs in a group
SELECT COUNT(*) as total
FROM log
WHERE group_id = ?;

-- name: CountLogsByGroupIDSince :one
-- Count logs in a group created after a timestamp (for the "+N new records"
-- indicator on the log group summary).
SELECT COUNT(*) as total
FROM log
WHERE group_id = ?
  AND created_at > ?;

-- name: DeleteOldLogs :exec
DELETE FROM log
WHERE created_at < ?;

-- name: RecalculateLogGroupCounters :exec
-- Recalculate last_log_at for all groups after log deletion
UPDATE log_group
SET last_log_at = COALESCE((SELECT MAX(created_at) FROM log l WHERE l.group_id = log_group.id), 0);

-- name: GetLogByID :one
SELECT id, trace_id, level, message, context, group_id, created_at FROM log
WHERE id = ?;

-- name: GetPercentiles :one
SELECT
    COUNT(*) as total,
    (SELECT COUNT(*) FROM log l2 WHERE LOWER(l2.level) = 'error' AND l2.created_at >= ?1) as errors,
    (SELECT COUNT(*) FROM log l3 WHERE LOWER(l3.level) = 'warn' AND l3.created_at >= ?1) as warnings,
    (SELECT COUNT(*) FROM log l4 WHERE LOWER(l4.level) = 'info' AND l4.created_at >= ?1) as infos
FROM log l1
WHERE l1.created_at >= ?1;

-- name: HasLog :one
-- Whether the app ever received a log.
SELECT EXISTS(SELECT 1 FROM log) AS has_log;

-- name: GetLatestLogAt :one
SELECT CAST(COALESCE(MAX(created_at), 0) AS INTEGER) FROM log;

-- name: GetLogVolumeTimeSeries :many
-- Get log volume over time bucketed by intervals and grouped by level
SELECT
    CASE
        WHEN sqlc.arg(granularity) = '1min' THEN strftime('%Y-%m-%d %H:%M:00', datetime(created_at, 'unixepoch'))
        WHEN sqlc.arg(granularity) = '5min' THEN
            strftime('%Y-%m-%d %H:', datetime(created_at, 'unixepoch')) ||
            printf('%02d', (CAST(strftime('%M', datetime(created_at, 'unixepoch')) AS INTEGER) / 5) * 5) || ':00'
        WHEN sqlc.arg(granularity) = '15min' THEN
            strftime('%Y-%m-%d %H:', datetime(created_at, 'unixepoch')) ||
            printf('%02d', (CAST(strftime('%M', datetime(created_at, 'unixepoch')) AS INTEGER) / 15) * 15) || ':00'
        WHEN sqlc.arg(granularity) = '1hour' THEN strftime('%Y-%m-%d %H:00:00', datetime(created_at, 'unixepoch'))
    END as time_bucket,
    LOWER(level) as level,
    COUNT(*) as count
FROM log
WHERE created_at >= sqlc.arg(since)
GROUP BY time_bucket, LOWER(level)
ORDER BY time_bucket ASC, level;

-- name: CountLogsSince :one
-- Total log count since a given timestamp
SELECT COUNT(*) as total
FROM log
WHERE created_at >= sqlc.arg(since);

-- name: DeleteLogGroup :execrows
-- Delete a log group by ID
DELETE FROM log_group WHERE id = ?;
