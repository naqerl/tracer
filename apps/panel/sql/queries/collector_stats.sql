-- name: InsertSystemStats :exec
INSERT INTO system_stats (
    collector_name, cpu_usage_percent, mem_total_mb, mem_used_mb,
    mem_available_mb, disk_total_gb, disk_used_gb, disk_avail_gb, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetLatestStats :one
SELECT id, collector_name, cpu_usage_percent, mem_total_mb, mem_used_mb, 
       mem_available_mb, disk_total_gb, disk_used_gb, disk_avail_gb, created_at
FROM system_stats
ORDER BY created_at DESC
LIMIT 1;

-- name: GetStatsHistory :many
SELECT id, collector_name, cpu_usage_percent, mem_total_mb, mem_used_mb, 
       mem_available_mb, disk_total_gb, disk_used_gb, disk_avail_gb, created_at
FROM system_stats
WHERE created_at >= ?
ORDER BY created_at ASC;

-- name: DeleteOldStats :exec
DELETE FROM system_stats WHERE created_at < ?;
