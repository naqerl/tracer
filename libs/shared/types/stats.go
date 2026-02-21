package types

import "time"

// Stats contains system statistics, shared between collector and panel via API
type Stats struct {
	CPUUsagePercent float64   `json:"cpu_usage_percent"`
	MemTotalMB      uint64    `json:"mem_total_mb"`
	MemUsedMB       uint64    `json:"mem_used_mb"`
	MemAvailableMB  uint64    `json:"mem_available_mb"`
	DiskTotalGB     uint64    `json:"disk_total_gb"`
	DiskUsedGB      uint64    `json:"disk_used_gb"`
	DiskAvailGB     uint64    `json:"disk_avail_gb"`
	Timestamp       time.Time `json:"timestamp"`
}
