package stats

type Spec struct {
	RawStatsInterval string `toml:"stats_interval"`
	CPUAlertPercent  float64 `toml:"cpu_alert_percent"`
	MemAlertPercent  float64 `toml:"mem_alert_percent"`
	DiskAlertPercent float64 `toml:"disk_alert_percent"`

        StatsInterval    time.Duration        
}
