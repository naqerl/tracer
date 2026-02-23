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
