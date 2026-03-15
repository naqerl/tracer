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

-- name: DeleteExpiredSessions :exec
DELETE FROM session
WHERE expires_at < ?;
