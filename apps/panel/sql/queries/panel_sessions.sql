-- Session queries

-- name: CreateSession :exec
INSERT INTO session (id, user_id, expires_at, created_at)
VALUES (?, ?, ?, ?);

-- name: GetSession :one
SELECT * FROM session
WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM session
WHERE id = ?;

-- name: DeleteExpiredSessions :exec
DELETE FROM session
WHERE expires_at < ?;

-- name: RefreshSession :exec
UPDATE session
SET expires_at = ?
WHERE id = ?;

-- name: DeleteSessionsByUser :exec
DELETE FROM session
WHERE user_id = ?;
