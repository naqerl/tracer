-- name: SaveHealthPeriod :exec
INSERT INTO app_health_period (status, started_at, ended_at)
VALUES (?, ?, ?);

-- name: GetLatestHealthPeriod :one
SELECT id, status, started_at, ended_at
FROM app_health_period
ORDER BY started_at DESC
LIMIT 1;

-- name: GetOpenHealthPeriod :one
SELECT id, status, started_at, ended_at
FROM app_health_period
WHERE ended_at IS NULL
ORDER BY started_at DESC
LIMIT 1;

-- name: CloseHealthPeriod :exec
UPDATE app_health_period
SET ended_at = ?
WHERE id = ?;

-- name: ListHealthPeriodsOverlappingRange :many
SELECT id, status, started_at, ended_at
FROM app_health_period
WHERE started_at <= ?
  AND (ended_at IS NULL OR ended_at >= ?)
ORDER BY started_at ASC;

-- name: DeleteOldHealthPeriods :exec
DELETE FROM app_health_period
WHERE ended_at IS NOT NULL
  AND ended_at < ?;
