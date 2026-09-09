package stats

import (
	"context"
	"time"

	"tracer/libs/types"
)

type Spec struct {
	StatsInterval time.Duration
	CPU           bool
	Mem           bool
	Disk          bool
}

type Stats struct {
	Timestamp time.Time

	CPUUsagePercent float64

	MemTotalMB     int64
	MemUsedMB      int64
	MemAvailableMB int64

	DiskTotalGB int64
	DiskUsedGB  int64
	DiskAvailGB int64
}

type ResourceMonitor struct {
	spec Spec
}

func New(spec Spec) ResourceMonitor {
	if spec.StatsInterval <= 0 {
		spec.StatsInterval = 30 * time.Second
	}
	return ResourceMonitor{
		spec: spec,
	}
}

func (rm ResourceMonitor) Produce(ctx context.Context) <-chan types.Event {
	out := make(chan types.Event, 10)

	go func() {
		defer close(out)
		ticker := time.NewTicker(rm.spec.StatsInterval)
		defer ticker.Stop()

		var stats SystemStats
		for {
			select {
			case <-ticker.C:
				snapshot, err := stats.Snapshot(rm.spec)
				if err == nil {
					out <- types.StatsEvent{
						Time: snapshot.Timestamp,
						Data: types.Stats{
							CPUUsagePercent: snapshot.CPUUsagePercent,
							MemTotalMB:      snapshot.MemTotalMB,
							MemUsedMB:       snapshot.MemUsedMB,
							MemAvailableMB:  snapshot.MemAvailableMB,
							DiskTotalGB:     snapshot.DiskTotalGB,
							DiskUsedGB:      snapshot.DiskUsedGB,
							DiskAvailGB:     snapshot.DiskAvailGB,
							Timestamp:       snapshot.Timestamp,
						},
					}
				} else {
					out <- types.ErrorEvent{
						Time:    time.Now().UTC(),
						ErrType: "stats_snapshot_failed",
						Source:  "resource_monitor",
						Message: err.Error(),
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	return out
}
