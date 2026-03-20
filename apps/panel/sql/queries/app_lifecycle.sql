-- name: InsertLifecycleEvent :one
INSERT INTO lifecycle_event (kind, raw_line, created_at)
VALUES (?, ?, ?)
RETURNING *;
