-- name: InsertLog :one
INSERT INTO logs (trace_id, level, message, source, context, raw, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

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

-- name: CountByLevelAndHour :many
SELECT 
    level,
    strftime('%Y-%m-%d %H:00:00', created_at) as hour,
    COUNT(*) as count
FROM logs
WHERE created_at >= ?
GROUP BY level, hour
ORDER BY hour;

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
    (SELECT COUNT(*) FROM logs l2 WHERE l2.level = 'error' AND l2.created_at >= ?1) as errors,
    (SELECT COUNT(*) FROM logs l3 WHERE l3.level = 'warn' AND l3.created_at >= ?1) as warnings,
    (SELECT COUNT(*) FROM logs l4 WHERE l4.level = 'info' AND l4.created_at >= ?1) as infos
FROM logs l1
WHERE l1.created_at >= ?1;
