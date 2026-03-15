-- name: InsertLifecycleEvent :one
INSERT INTO lifecycle_events (kind, raw_line, created_at)
VALUES (?, ?, ?)
RETURNING *;
