-- name: InsertLifecycleEvent :one
INSERT INTO lifecycle_events (kind, raw_line, created_at)
VALUES (?, ?, ?)
RETURNING *;

-- name: ListRecentLifecycleEvents :many
SELECT id, kind, raw_line, created_at
FROM lifecycle_events
ORDER BY created_at DESC
LIMIT ?;
