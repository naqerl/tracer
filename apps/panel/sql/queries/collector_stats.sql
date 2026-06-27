-- CPU Period
-- name: GetOpenCPUPeriod :one
SELECT * FROM cpu_period WHERE ended_at IS NULL ORDER BY started_at DESC LIMIT 1;

-- name: CloseCPUPeriod :exec
UPDATE cpu_period SET ended_at = ? WHERE id = ?;

-- name: InsertCPUPeriod :one
INSERT INTO cpu_period (collector_name, usage_percent, started_at, ended_at)
VALUES (?, ?, ?, NULL)
RETURNING *;

-- name: GetCPUHistory :many
SELECT * FROM cpu_period
WHERE (ended_at IS NULL OR ended_at >= ?)
ORDER BY started_at ASC;

-- name: DeleteOldCPUPeriods :exec
DELETE FROM cpu_period WHERE ended_at IS NOT NULL AND ended_at < ?;

-- Memory Period
-- name: GetOpenMemPeriod :one
SELECT * FROM mem_period WHERE ended_at IS NULL ORDER BY started_at DESC LIMIT 1;

-- name: CloseMemPeriod :exec
UPDATE mem_period SET ended_at = ? WHERE id = ?;

-- name: InsertMemPeriod :one
INSERT INTO mem_period (collector_name, total_mb, used_mb, available_mb, started_at, ended_at)
VALUES (?, ?, ?, ?, ?, NULL)
RETURNING *;

-- name: GetMemHistory :many
SELECT * FROM mem_period
WHERE (ended_at IS NULL OR ended_at >= ?)
ORDER BY started_at ASC;

-- name: DeleteOldMemPeriods :exec
DELETE FROM mem_period WHERE ended_at IS NOT NULL AND ended_at < ?;

-- Disk Period
-- name: GetOpenDiskPeriod :one
SELECT * FROM disk_period WHERE ended_at IS NULL ORDER BY started_at DESC LIMIT 1;

-- name: CloseDiskPeriod :exec
UPDATE disk_period SET ended_at = ? WHERE id = ?;

-- name: InsertDiskPeriod :one
INSERT INTO disk_period (collector_name, total_gb, used_gb, avail_gb, started_at, ended_at)
VALUES (?, ?, ?, ?, ?, NULL)
RETURNING *;

-- name: GetDiskHistory :many
SELECT * FROM disk_period
WHERE (ended_at IS NULL OR ended_at >= ?)
ORDER BY started_at ASC;

-- name: DeleteOldDiskPeriods :exec
DELETE FROM disk_period WHERE ended_at IS NOT NULL AND ended_at < ?;
