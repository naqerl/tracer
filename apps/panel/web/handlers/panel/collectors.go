package panel

import (
	"net/http"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/services/ingest"
	"github.com/ai-shift/tracer/apps/panel/web/middleware"
	"github.com/ai-shift/tracer/apps/panel/web/templates"
)

func (h *handler) collectorOverviewPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	collectorName := r.PathValue("collector")
	if collectorName == "" {
		h.t.RenderError(w, r, "Error", "Collector name required", http.StatusBadRequest)
		return
	}

	if !h.userHasCollectorAccess(r, user, collectorName) {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
		return
	}

	data := map[string]any{
		"Title":         collectorName,
		"CollectorName": collectorName,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add(collectorName, collectorName).
			Build(),
	}

	if middleware.IsMarkdownFormat(r) {
		collector, exists := h.tracker.Get(collectorName)
		if !exists {
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
			return
		}

		var stats *db.SystemStat
		var statsDisplay *StatsDisplay
		s, err := h.ingestService.GetLatestStats(r.Context(), ingest.CollectorName(collectorName))
		if err == nil {
			stats = &s
			statsDisplay = formatStatsForDisplay(s)
		}

		data["IsConnected"] = collector.IsConnected
		data["Stats"] = stats
		data["StatsDisplay"] = statsDisplay
		data["Apps"] = h.filterAppsByAccess(r, user, collector.Apps)
		h.t.RenderMarkdown(w, r, "collector-overview-markdown", data)
		return
	}

	h.t.RenderHTTP(w, r, "collector-overview", data)
}

func (h *handler) collectorOverviewData(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	if collectorName == "" {
		http.Error(w, "Collector name required", http.StatusBadRequest)
		return
	}
	if !h.userHasCollectorAccess(r, user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	collector, exists := h.tracker.Get(collectorName)
	if !exists {
		http.Error(w, "Collector not found", http.StatusNotFound)
		return
	}

	var stats *db.SystemStat
	var statsDisplay *StatsDisplay
	s, err := h.ingestService.GetLatestStats(r.Context(), ingest.CollectorName(collectorName))
	if err == nil {
		stats = &s
		statsDisplay = formatStatsForDisplay(s)
	}

	h.t.RenderHTTP(w, r, "collector-overview-data", map[string]any{
		"CollectorName": collectorName,
		"IsConnected":   collector.IsConnected,
		"Stats":         stats,
		"StatsDisplay":  statsDisplay,
		"Apps":          h.filterAppsByAccess(r, user, collector.Apps),
	})
}

func (h *handler) collectorResourcesPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	collectorName := r.PathValue("collector")
	if collectorName == "" {
		h.t.RenderError(w, r, "Error", "Collector name required", http.StatusBadRequest)
		return
	}
	if !h.userHasCollectorAccess(r, user, collectorName) {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	data := map[string]any{
		"Title":         collectorName + " Resources",
		"CollectorName": collectorName,
		"RangeParam":    rangeParam,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add(collectorName, collectorName).
			Add("resources", "resources").
			Build(),
	}

	h.t.RenderHTTP(w, r, "collector-stats", data)
}

func (h *handler) collectorResourcesEntries(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	if collectorName == "" {
		http.Error(w, "Collector name required", http.StatusBadRequest)
		return
	}
	if !h.userHasCollectorAccess(r, user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	stats, err := h.ingestService.GetStatsHistory(r.Context(), ingest.CollectorName(collectorName), parseTimeRange(rangeParam))
	if err != nil {
		http.Error(w, "Failed to get stats history", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "collector-stats-data", map[string]any{
		"CollectorName": collectorName,
		"RangeParam":    rangeParam,
		"Stats":         stats,
	})
}

func (h *handler) collectorAppsPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	collectorName := r.PathValue("collector")
	if collectorName == "" {
		h.t.RenderError(w, r, "Error", "Collector name required", http.StatusBadRequest)
		return
	}
	if !h.userHasCollectorAccess(r, user, collectorName) {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
		return
	}

	h.t.RenderHTTP(w, r, "collector-apps", map[string]any{
		"Title":         collectorName + " Apps",
		"CollectorName": collectorName,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add(collectorName, collectorName).
			Add("apps", "apps").
			Build(),
	})
}

func (h *handler) collectorAppsEntries(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	if collectorName == "" {
		http.Error(w, "Collector name required", http.StatusBadRequest)
		return
	}
	if !h.userHasCollectorAccess(r, user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	collector, exists := h.tracker.Get(collectorName)
	if !exists {
		http.Error(w, "Collector not found", http.StatusNotFound)
		return
	}

	h.t.RenderHTTP(w, r, "collector-apps-data", map[string]any{
		"CollectorName": collectorName,
		"Apps":          h.filterAppsByAccess(r, user, collector.Apps),
	})
}
