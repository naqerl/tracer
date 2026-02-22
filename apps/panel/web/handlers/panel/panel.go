package panel

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/services/ingest"
	panelsvc "github.com/ai-shift/tracer/apps/panel/internal/services/panel"
	"github.com/ai-shift/tracer/apps/panel/internal/services/permissions"
	"github.com/ai-shift/tracer/apps/panel/web/middleware"
	authmw "github.com/ai-shift/tracer/apps/panel/web/middleware/auth"
	"github.com/ai-shift/tracer/apps/panel/web/templates"
	sharedtypes "github.com/ai-shift/tracer/libs/types"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t             templates.Templates
	panelSvc      *panelsvc.Service
	permissions   *permissions.Service
	ingestService *ingest.Service
	tracker       *ingest.Tracker
}

func InitMux(panelSvc *panelsvc.Service, permissions *permissions.Service, ingestService *ingest.Service, tracker *ingest.Tracker) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:             templates.New(views),
		panelSvc:      panelSvc,
		permissions:   permissions,
		ingestService: ingestService,
		tracker:       tracker,
	}

	// Dashboard
	m.HandleFunc("GET /", h.dashboardPage)
	m.HandleFunc("GET /collectors", h.dashboardPage)
	m.HandleFunc("GET /collectors/entries", h.dashboardCollectors)

	// Collector
	m.HandleFunc("GET /collectors/{collector}", h.collectorOverviewPage)
	m.HandleFunc("GET /collectors/{collector}/overview", h.collectorOverviewData)
	m.HandleFunc("GET /collectors/{collector}/resources", h.collectorResourcesPage)
	m.HandleFunc("GET /collectors/{collector}/resources/entries", h.collectorResourcesEntries)
	m.HandleFunc("GET /collectors/{collector}/apps", h.collectorAppsPage)
	m.HandleFunc("GET /collectors/{collector}/apps/entries", h.collectorAppsEntries)

	// App logs
	m.HandleFunc("GET /collectors/{collector}/apps/{app}", h.appAnalyticsPage)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/recent", h.appRecentLogs)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs", h.appLogsPage)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/entries", h.appLogGroupsEntries)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups/{groupID}", h.appLogGroupPage)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups/{groupID}/entries", h.appLogGroupEntries)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/{id}", h.getLogByID)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/volume", h.appLogsVolume)

	return m
}

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

// Dashboard handlers

func (h *handler) dashboardPage(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireAuthRedirect(w, r)
	if err != nil {
		return
	}

	collectorsWithStats, err := h.getFilteredCollectors(r, user)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":      "Panel",
		"User":       user,
		"IsAdmin":    user.IsAdmin,
		"Nav":        templates.NewNavBuilder().Add("panel", "panel").Build(),
		"Collectors": collectorsWithStats,
	}

	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "dashboard-markdown", data)
		return
	}

	h.t.RenderHTTP(w, r, "dashboard", data)
}

func (h *handler) dashboardCollectors(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.GetUser(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorsWithStats, err := h.getFilteredCollectors(r, user)
	if err != nil {
		http.Error(w, "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "collectors-sections", map[string]any{"Collectors": collectorsWithStats})
}

// Collector handlers

func (h *handler) collectorOverviewPage(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireAuthRedirect(w, r)
	if err != nil {
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
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
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
	user, err := authmw.GetUser(r)
	if err != nil {
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
	user, err := authmw.RequireAuthRedirect(w, r)
	if err != nil {
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
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(collectorName, collectorName).
			Add("resources", "resources").
			Build(),
	}

	h.t.RenderHTTP(w, r, "collector-stats", data)
}

func (h *handler) collectorResourcesEntries(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.GetUser(r)
	if err != nil {
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
	user, err := authmw.RequireAuthRedirect(w, r)
	if err != nil {
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
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(collectorName, collectorName).
			Add("apps", "apps").
			Build(),
	})
}

func (h *handler) collectorAppsEntries(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.GetUser(r)
	if err != nil {
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

// App logs handlers

func (h *handler) appAnalyticsPage(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireAuthRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	if middleware.IsMarkdownFormat(r) {
		stats, grouped, err := h.getAppAnalyticsData(r.Context(), collectorName, appName)
		if err != nil {
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
			return
		}
		h.t.RenderMarkdown(w, r, "app-analytics-markdown", map[string]any{
			"Title":         appName + " Analytics",
			"AppName":       appName,
			"CollectorName": collectorName,
			"Stats":         stats,
			"Grouped":       grouped,
		})
		return
	}

	h.t.RenderHTTP(w, r, "app-page", map[string]any{
		"Title":         appName + " Analytics",
		"AppName":       appName,
		"CollectorName": collectorName,
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(collectorName, collectorName).
			Add("apps", "apps").
			Add(appName, appName).
			Build(),
	})
}

func (h *handler) appRecentLogs(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.GetUser(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	_, grouped, err := h.getAppAnalyticsData(r.Context(), collectorName, appName)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	h.t.RenderHTTP(w, r, "app-logs", map[string]any{
		"AppName":       appName,
		"CollectorName": collectorName,
		"Grouped":       grouped,
	})
}

func (h *handler) appLogsPage(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireAuthRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	if middleware.IsMarkdownFormat(r) {
		logGroups, err := h.panelSvc.GetLogGroups(r.Context(), collectorName, appName, "", 0, 50)
		if err != nil {
			if err == panelsvc.ErrAppNotFound {
				h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
				return
			}
			h.t.RenderError(w, r, "Error", "Failed to fetch log groups", http.StatusInternalServerError)
			return
		}
		h.t.RenderMarkdown(w, r, "app-logs-markdown", map[string]any{
			"AppName":       appName,
			"CollectorName": collectorName,
			"Groups":        logGroups.Groups,
			"TotalCount":    logGroups.TotalCount,
			"ErrorCount":    logGroups.ErrorCount,
			"WarnCount":     logGroups.WarnCount,
			"InfoCount":     logGroups.InfoCount,
		})
		return
	}

	h.t.RenderHTTP(w, r, "app-logs-page", map[string]any{
		"Title":         appName + " Logs",
		"AppName":       appName,
		"CollectorName": collectorName,
		"User":          user,
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(collectorName, collectorName).
			Add("apps", "apps").
			Add(appName, appName).
			Add("logs", "logs").
			Build(),
	})
}

func (h *handler) appLogGroupsEntries(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.GetUser(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	level := r.URL.Query().Get("level")
	offset := parseOffset(r.URL.Query().Get("offset"))
	limit := int64(50)
	logGroups, err := h.panelSvc.GetLogGroups(r.Context(), collectorName, appName, level, offset, limit)
	if err != nil {
		if err == panelsvc.ErrAppNotFound {
			http.Error(w, "App not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to fetch log groups", http.StatusInternalServerError)
		return
	}

	currentCount := logGroups.TotalCount
	if level != "" {
		switch level {
		case "error":
			currentCount = logGroups.ErrorCount
		case "warn":
			currentCount = logGroups.WarnCount
		case "info":
			currentCount = logGroups.InfoCount
		}
	}

	h.t.RenderHTTP(w, r, "app-logs-table", map[string]any{
		"AppName":       appName,
		"CollectorName": collectorName,
		"Groups":        logGroups.Groups,
		"Level":         level,
		"TotalCount":    logGroups.TotalCount,
		"ErrorCount":    logGroups.ErrorCount,
		"WarnCount":     logGroups.WarnCount,
		"InfoCount":     logGroups.InfoCount,
		"Offset":        offset,
		"Limit":         limit,
		"HasMore":       offset+limit < currentCount,
		"NextOffset":    offset + limit,
	})
}

func (h *handler) appLogGroupPage(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireAuthRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
		return
	}

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	detail, err := h.panelSvc.GetLogDetail(r.Context(), collectorName, appName, groupID, 0, 1)
	if err != nil {
		if err == panelsvc.ErrAppNotFound || err == panelsvc.ErrLogNotFound {
			h.t.RenderError(w, r, "Error", "Group not found", http.StatusNotFound)
			return
		}
		h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
		return
	}

	highlightLogID := parseOffset(r.URL.Query().Get("logID"))

	h.t.RenderHTTP(w, r, "app-logs-detail-page", map[string]any{
		"Title":          appName + " Log Details",
		"AppName":        appName,
		"CollectorName":  collectorName,
		"LogGroup":       detail.Group,
		"HighlightLogID": highlightLogID,
		"User":           user,
		"IsAdmin":        user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(collectorName, collectorName).
			Add("apps", "apps").
			Add(appName, appName).
			Add("logs", "logs").
			Build(),
	})
}

func (h *handler) appLogGroupEntries(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.GetUser(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid group ID", http.StatusBadRequest)
		return
	}

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	offset := parseOffset(r.URL.Query().Get("offset"))
	highlightLogID := parseOffset(r.URL.Query().Get("logID"))
	limit := int64(50)
	detail, err := h.panelSvc.GetLogDetail(r.Context(), collectorName, appName, groupID, offset, limit)
	if err != nil {
		if err == panelsvc.ErrAppNotFound || err == panelsvc.ErrLogNotFound {
			http.Error(w, "Group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to fetch logs", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "app-logs-detail", map[string]any{
		"AppName":        appName,
		"CollectorName":  collectorName,
		"LogGroup":       detail.Group,
		"Logs":           detail.Logs,
		"Offset":         offset,
		"Limit":          limit,
		"HasMore":        offset+int64(len(detail.Logs)) < detail.TotalCount,
		"NextOffset":     offset + limit,
		"HighlightLogID": highlightLogID,
	})
}

func (h *handler) appLogsVolume(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.GetUser(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	timeSeriesData, err := h.panelSvc.GetLogVolumeTimeSeries(r.Context(), collectorName, appName, parseTimeRange(rangeParam), getBucketGranularity(rangeParam))
	if err != nil {
		if err == panelsvc.ErrAppNotFound {
			http.Error(w, "App not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to fetch chart data", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "app-logs-chart-data", map[string]any{
		"AppName":       appName,
		"CollectorName": collectorName,
		"TimeSeries":    timeSeriesData,
		"RangeParam":    rangeParam,
	})
}

func (h *handler) getLogByID(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.GetUser(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	idStr := r.PathValue("id")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid log ID", http.StatusBadRequest)
		return
	}

	logEntry, err := h.panelSvc.GetLogByID(r.Context(), collectorName, appName, id)
	if err != nil {
		if err == panelsvc.ErrAppNotFound {
			http.Error(w, "App not found", http.StatusNotFound)
			return
		}
		if err == panelsvc.ErrLogNotFound {
			http.Error(w, "Log not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to get log", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/panel/collectors/%s/apps/%s/logs/groups/%d?logID=%d", collectorName, appName, logEntry.GroupID.Int64, logEntry.ID), http.StatusSeeOther)
}

// Utility functions

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

// TODO: Rework
func (h *handler) getFilteredCollectors(r *http.Request, user sharedtypes.User) ([]CollectorWithStats, error) {
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

func (h *handler) userHasCollectorAccess(r *http.Request, user sharedtypes.User, collectorName string) bool {
	hasAccess, err := h.permissions.CanAccessCollector(r.Context(), user, collectorName)
	return err == nil && hasAccess
}

func (h *handler) filterAppsByAccess(r *http.Request, user sharedtypes.User, apps []string) []string {
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

func (h *handler) getAppAnalyticsData(ctx context.Context, collectorName, appName string) (*panelsvc.AppStats, any, error) {
	stats, grouped, err := h.panelSvc.GetAppAnalytics(ctx, collectorName, appName)
	if err != nil {
		if err == panelsvc.ErrAppNotFound {
			return nil, nil, fmt.Errorf("app not found")
		}
		return nil, nil, err
	}
	return stats, grouped, nil
}

func parseOffset(offsetParam string) int64 {
	if offsetParam == "" {
		return 0
	}
	offset, err := strconv.ParseInt(offsetParam, 10, 64)
	if err != nil {
		return 0
	}
	return offset
}
