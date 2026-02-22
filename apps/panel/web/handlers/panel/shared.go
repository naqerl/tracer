package panel

import (
	"net/http"
	"time"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/services/ingest"
)

// StatsDisplay holds formatted stats data for display
// and freshness state for UI hints.
type StatsDisplay struct {
	db.SystemStat
	TimestampFormatted string
	Staleness          string
}

// CollectorWithStats combines collector info with latest system stats.
type CollectorWithStats struct {
	ingest.Collector
	Stats        db.SystemStat
	StatsDisplay *StatsDisplay
}

func formatStatsForDisplay(stats db.SystemStat) *StatsDisplay {
	now := time.Now()
	age := now.Sub(stats.CreatedAt)

	nowY, nowM, nowD := now.Date()
	statY, statM, statD := stats.CreatedAt.Date()
	sameDate := nowY == statY && nowM == statM && nowD == statD

	var staleness string
	var formatted string

	if !sameDate {
		staleness = "old"
		formatted = stats.CreatedAt.Format("2006-01-02 15:04:05")
	} else if age > 30*time.Minute {
		staleness = "stale"
		formatted = stats.CreatedAt.Format("15:04:05")
	} else if age > 5*time.Minute {
		staleness = "warning"
		formatted = stats.CreatedAt.Format("15:04:05")
	} else {
		staleness = "fresh"
		formatted = stats.CreatedAt.Format("15:04:05")
	}

	return &StatsDisplay{
		SystemStat:         stats,
		TimestampFormatted: formatted,
		Staleness:          staleness,
	}
}

func (h *handler) getFilteredCollectors(r *http.Request, user *db.User) ([]CollectorWithStats, error) {
	collectorList := h.tracker.List()
	collectorsWithStats := make([]CollectorWithStats, 0, len(collectorList))

	for _, c := range collectorList {
		hasAccess, err := h.permissions.CanAccessCollector(r.Context(), user, c.Name)
		if err != nil {
			return nil, err
		}
		if !hasAccess {
			continue
		}

		cws := CollectorWithStats{Collector: c}
		stats, err := h.ingestService.GetLatestStats(r.Context(), ingest.CollectorName(c.Name))
		if err == nil {
			cws.Stats = stats
			cws.StatsDisplay = formatStatsForDisplay(stats)
		}
		collectorsWithStats = append(collectorsWithStats, cws)
	}

	return collectorsWithStats, nil
}

func (h *handler) userHasCollectorAccess(r *http.Request, user *db.User, collectorName string) bool {
	hasAccess, err := h.permissions.CanAccessCollector(r.Context(), user, collectorName)
	return err == nil && hasAccess
}

func (h *handler) filterAppsByAccess(r *http.Request, user *db.User, apps []string) []string {
	filtered, err := h.permissions.FilterAppsByAccess(r.Context(), user, apps)
	if err != nil {
		return nil
	}
	return filtered
}

func parseTimeRange(rangeParam string) time.Time {
	if rangeParam == "" {
		return time.Now().Add(-24 * time.Hour)
	}

	var value int
	var unit byte
	if len(rangeParam) < 2 {
		return time.Now().Add(-24 * time.Hour)
	}

	for i := 0; i < len(rangeParam); i++ {
		if rangeParam[i] >= '0' && rangeParam[i] <= '9' {
			value = value*10 + int(rangeParam[i]-'0')
		} else {
			unit = rangeParam[i]
			break
		}
	}

	switch unit {
	case 'h':
		return time.Now().Add(-time.Duration(value) * time.Hour)
	case 'd':
		return time.Now().Add(-time.Duration(value) * 24 * time.Hour)
	default:
		return time.Now().Add(-24 * time.Hour)
	}
}

func getBucketGranularity(rangeParam string) string {
	switch rangeParam {
	case "1h":
		return "1min"
	case "6h", "24h":
		return "5min"
	case "7d":
		return "15min"
	case "30d":
		return "1hour"
	default:
		return "5min"
	}
}
