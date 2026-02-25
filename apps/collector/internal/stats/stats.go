package stats

import (
	"context"
	"time"

	"github.com/ai-shift/tracer/libs/types"
)

type Spec struct {
	StatsInterval time.Duration
}

type Stats struct {
	Timestamp time.Time

	CPUUsagePercent float64

	MemTotalMB     uint64
	MemUsedMB      uint64
	MemAvailableMB uint64

	DiskTotalGB uint64
	DiskUsedGB  uint64
	DiskAvailGB uint64
}

type ResourceMonitor struct {
	spec Spec
}

func New(spec Spec) ResourceMonitor {
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
				_, err := stats.Snapshot()
				if err == nil {
					out <- types.StatsEvent{}
					panic("not implemented")
				} else {
					out <- types.ErrorEvent{
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
