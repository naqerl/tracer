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
	"github.com/ai-shift/tracer/apps/panel/internal/services/panelread"
	"github.com/ai-shift/tracer/apps/panel/internal/services/permissions"
	"github.com/ai-shift/tracer/apps/panel/web/htmx"
	"github.com/ai-shift/tracer/apps/panel/web/middleware"
	"github.com/ai-shift/tracer/apps/panel/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t             templates.Templates
	panelRead     *panelread.Service
	permissions   *permissions.Service
	ingestService *ingest.Service
	tracker       *ingest.Tracker
}

func InitMux(panelRead *panelread.Service, permissions *permissions.Service, ingestService *ingest.Service, tracker *ingest.Tracker) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:             templates.New(views),
		panelRead:     panelRead,
		permissions:   permissions,
		ingestService: ingestService,
		tracker:       tracker,
	}

	// Dashboard - list all ingests
	m.HandleFunc("GET /", h.dashboard)
	m.HandleFunc("GET /collectors", h.getCollectors)

	// Collector routes
	m.HandleFunc("GET /{collector}", h.collectorOverview)
	m.HandleFunc("GET /{collector}/resources", h.collectorResources)
	m.HandleFunc("GET /{collector}/apps", h.collectorApps)
	m.HandleFunc("GET /{collector}/apps/{app}", h.appAnalytics)
	m.HandleFunc("GET /{collector}/apps/{app}/logs", h.appLogs)
	m.HandleFunc("GET /{collector}/apps/{app}/logs/{id}", h.getLogByID)
	m.HandleFunc("GET /{collector}/apps/{app}/logs/chart-data", h.appLogsChartData)

	return m
}

// dashboard shows all collectors overview
func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	// Get ingests filtered by user access
	collectorsWithStats, err := h.getFilteredCollectors(r, user)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	nav := templates.NewNavBuilder().Add("analytics", "analytics").Build()

	isAdmin := user.IsAdmin == 1

	data := map[string]any{
		"Title":      "Analytics",
		"User":       user,
		"IsAdmin":    isAdmin,
		"Nav":        nav,
		"Collectors": collectorsWithStats,
	}

	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "dashboard-markdown", data)
	} else if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "dashboard-content", data)
	} else {
		h.t.RenderHTTP(w, r, "dashboard", data)
	}
}

// getCollectors returns ingests sections for HTMX refresh
func (h *handler) getCollectors(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorsWithStats, err := h.getFilteredCollectors(r, user)
	if err != nil {
		http.Error(w, "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "collectors-sections", map[string]any{
		"Collectors": collectorsWithStats,
	})
}

// collectorOverview shows a single ingest with stats and apps
func (h *handler) collectorOverview(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	collectorName := r.PathValue("collector")
	if collectorName == "" {
		h.t.RenderError(w, r, "Error", "Collector name required", http.StatusBadRequest)
		return
	}

	// Check access
	if !h.userHasCollectorAccess(r, user, collectorName) {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
		return
	}

	nav := templates.NewNavBuilder().
		Add("analytics", "analytics").
		Add(collectorName, collectorName).
		Build()

	isAdmin := user.IsAdmin == 1

	data := map[string]any{
		"Title":         collectorName,
		"CollectorName": collectorName,
		"User":          user,
		"IsAdmin":       isAdmin,
		"Nav":           nav,
	}
	if !htmx.IsAjax(r) && !middleware.IsMarkdownFormat(r) {
		h.t.RenderHTTP(w, r, "collector-overview", data)
		return
	}

	// AJAX or Markdown: fetch data
	collector, exists := h.tracker.Get(collectorName)
	if !exists {
		h.t.RenderError(w, r, "Error", "Ingest not found", http.StatusNotFound)
		return
	}

	// Get latest stats
	var stats *db.SystemStat
	var statsDisplay *StatsDisplay
	s, err := h.ingestService.GetLatestStats(r.Context(), ingest.CollectorName(collectorName))
	if err == nil {
		stats = &s
		statsDisplay = formatStatsForDisplay(s)
	}

	// Filter apps by user access
	apps := h.filterAppsByAccess(r, user, collector.Apps)

	data["IsConnected"] = collector.IsConnected
	data["Stats"] = stats
	data["StatsDisplay"] = statsDisplay
	data["Apps"] = apps

	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "collector-overview-markdown", data)
	} else {
		h.t.RenderHTTP(w, r, "collector-overview-data", data)
	}
}

// collectorResources shows resource usage charts (was collectorStats)
func (h *handler) collectorResources(w http.ResponseWriter, r *http.Request) {
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

	nav := templates.NewNavBuilder().
		Add("analytics", "analytics").
		Add(collectorName, collectorName).
		Add("resources", "resources").
		Build()

	since := parseTimeRange(rangeParam)
	stats, err := h.ingestService.GetStatsHistory(r.Context(), ingest.CollectorName(collectorName), since)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get stats history", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":         collectorName + " Resources",
		"CollectorName": collectorName,
		"RangeParam":    rangeParam,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav":           nav,
		"Stats":         stats,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "collector-stats-content", data)
	} else {
		h.t.RenderHTTP(w, r, "collector-stats", data)
	}
}

// collectorApps shows apps list for a collector
func (h *handler) collectorApps(w http.ResponseWriter, r *http.Request) {
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

	nav := templates.NewNavBuilder().
		Add("analytics", "analytics").
		Add(collectorName, collectorName).
		Add("apps", "apps").
		Build()

	ingest, exists := h.tracker.Get(collectorName)
	if !exists {
		h.t.RenderError(w, r, "Error", "Ingest not found", http.StatusNotFound)
		return
	}

	apps := h.filterAppsByAccess(r, user, ingest.Apps)

	data := map[string]any{
		"Title":         collectorName + " Apps",
		"CollectorName": collectorName,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav":           nav,
		"Apps":          apps,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "collector-apps-content", data)
	} else {
		h.t.RenderHTTP(w, r, "collector-apps", data)
	}
}

// appAnalytics shows analytics for a specific app
func (h *handler) appAnalytics(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	// Check app access
	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	stats, grouped, err := h.getAppAnalyticsData(r.Context(), collectorName, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		return
	}

	data := map[string]any{
		"Title":         appName + " Analytics",
		"AppName":       appName,
		"CollectorName": collectorName,
		"Stats":         stats,
		"Grouped":       grouped,
	}

	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "app-analytics-markdown", data)
		return
	}

	nav := templates.NewNavBuilder().
		Add("analytics", "analytics").
		Add(collectorName, collectorName).
		Add("apps", "apps").
		Add(appName, appName).
		Build()

	data["IsAdmin"] = user.IsAdmin == 1
	data["Nav"] = nav

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "app-logs", data)
		return
	}

	h.t.RenderHTTP(w, r, "app-page", data)
}

// Helper types and functions

// StatsDisplay holds formatted stats data for display
type StatsDisplay struct {
	db.SystemStat
	TimestampFormatted string // Formatted timestamp
	Staleness          string // "fresh", "warning", "stale", "old"
}

// CollectorWithStats combines collector info with latest system stats
type CollectorWithStats struct {
	ingest.Collector
	Stats        db.SystemStat
	StatsDisplay *StatsDisplay
}

// formatStatsForDisplay creates a StatsDisplay from SystemStat
func formatStatsForDisplay(stats db.SystemStat) *StatsDisplay {

	now := time.Now()
	age := now.Sub(stats.CreatedAt)

	// Check if same date
	nowY, nowM, nowD := now.Date()
	statY, statM, statD := stats.CreatedAt.Date()
	sameDate := nowY == statY && nowM == statM && nowD == statD

	var staleness string
	var formatted string

	if !sameDate {
		// Different date - old
		staleness = "old"
		formatted = stats.CreatedAt.Format("2006-01-02 15:04:05")
	} else if age > 30*time.Minute {
		// > 1 minute - stale
		staleness = "stale"
		formatted = stats.CreatedAt.Format("15:04:05")
	} else if age > 5*time.Minute {
		// > 30 seconds - warning
		staleness = "warning"
		formatted = stats.CreatedAt.Format("15:04:05")
	} else {
		// Fresh
		staleness = "fresh"
		formatted = stats.CreatedAt.Format("15:04:05")
	}

	return &StatsDisplay{
		SystemStat:         stats,
		TimestampFormatted: formatted,
		Staleness:          staleness,
	}
}

// getFilteredCollectors returns collectors filtered by user access
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

		cws := CollectorWithStats{
			Collector: c,
		}

		stats, err := h.ingestService.GetLatestStats(r.Context(), ingest.CollectorName(c.Name))
		if err == nil {
			cws.Stats = stats
			cws.StatsDisplay = formatStatsForDisplay(stats)
		}

		collectorsWithStats = append(collectorsWithStats, cws)
	}

	return collectorsWithStats, nil
}

// userHasCollectorAccess checks if user can access the collector
func (h *handler) userHasCollectorAccess(r *http.Request, user *db.User, collectorName string) bool {
	hasAccess, err := h.permissions.CanAccessCollector(r.Context(), user, collectorName)
	return err == nil && hasAccess
}

// filterAppsByAccess filters apps list by user access
func (h *handler) filterAppsByAccess(r *http.Request, user *db.User, apps []string) []string {
	filtered, err := h.permissions.FilterAppsByAccess(r.Context(), user, apps)
	if err != nil {
		return nil
	}
	return filtered
}

// appLogs shows logs for a specific app
func (h *handler) appLogs(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	// Check app access
	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	groupIDParam := r.URL.Query().Get("groupID")
	logIDParam := r.URL.Query().Get("logID")
	offsetParam := r.URL.Query().Get("offset")

	offset := int64(0)
	if offsetParam != "" {
		if parsedOffset, err := strconv.ParseInt(offsetParam, 10, 64); err == nil {
			offset = parsedOffset
		}
	}

	highlightLogID := int64(0)
	if logIDParam != "" {
		if parsedLogID, err := strconv.ParseInt(logIDParam, 10, 64); err == nil {
			highlightLogID = parsedLogID
		}
	}

	nav := templates.NewNavBuilder().
		Add("analytics", "analytics").
		Add(collectorName, collectorName).
		Add("apps", "apps").
		Add(appName, appName).
		Add("logs", "logs").
		Build()

	// Detail view: show individual logs for a group
	if groupIDParam != "" {
		groupID, err := strconv.ParseInt(groupIDParam, 10, 64)
		if err != nil {
			h.t.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
			return
		}

		limit := int64(50)
		detail, err := h.panelRead.GetLogDetail(r.Context(), collectorName, appName, groupID, offset, limit)
		if err != nil {
			if err == panelread.ErrAppNotFound || err == panelread.ErrLogNotFound {
				h.t.RenderError(w, r, "Error", "Group not found", http.StatusNotFound)
				return
			}
			h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
			return
		}

		data := map[string]any{
			"Title":          appName + " Log Details",
			"AppName":        appName,
			"CollectorName":  collectorName,
			"LogGroup":       detail.Group,
			"Logs":           detail.Logs,
			"User":           user,
			"IsAdmin":        user.IsAdmin == 1,
			"Nav":            nav,
			"Offset":         offset,
			"Limit":          limit,
			"HasMore":        offset+int64(len(detail.Logs)) < detail.TotalCount,
			"NextOffset":     offset + limit,
			"HighlightLogID": highlightLogID,
		}

		if middleware.IsMarkdownFormat(r) {
			h.t.RenderMarkdown(w, r, "app-logs-detail-markdown", data)
		} else if htmx.IsAjax(r) {
			h.t.RenderHTTP(w, r, "app-logs-detail", data)
		} else {
			h.t.RenderHTTP(w, r, "app-logs-detail-page", data)
		}
		return
	}

	// List view: show groups with counts
	level := r.URL.Query().Get("level")
	limit := int64(50)
	logGroups, err := h.panelRead.GetLogGroups(r.Context(), collectorName, appName, level, offset, limit)
	if err != nil {
		if err == panelread.ErrAppNotFound {
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
			return
		}
		h.t.RenderError(w, r, "Error", "Failed to fetch log groups", http.StatusInternalServerError)
		return
	}

	// Determine current count for HasMore calculation
	var currentCount int64
	if level != "" {
		switch level {
		case "error":
			currentCount = logGroups.ErrorCount
		case "warn":
			currentCount = logGroups.WarnCount
		case "info":
			currentCount = logGroups.InfoCount
		default:
			currentCount = logGroups.TotalCount
		}
	} else {
		currentCount = logGroups.TotalCount
	}

	data := map[string]any{
		"Title":         appName + " Logs",
		"AppName":       appName,
		"CollectorName": collectorName,
		"Groups":        logGroups.Groups,
		"Level":         level,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav":           nav,
		"TotalCount":    logGroups.TotalCount,
		"ErrorCount":    logGroups.ErrorCount,
		"WarnCount":     logGroups.WarnCount,
		"InfoCount":     logGroups.InfoCount,
		"Offset":        offset,
		"Limit":         limit,
		"HasMore":       offset+limit < currentCount,
		"NextOffset":    offset + limit,
	}

	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "app-logs-markdown", data)
	} else if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "app-logs-table", data)
	} else {
		h.t.RenderHTTP(w, r, "app-logs-page", data)
	}
}

// appLogsChartData returns time-series data for log volume chart
func (h *handler) appLogsChartData(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	// Check app access
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

	since := parseTimeRange(rangeParam)
	granularity := getBucketGranularity(rangeParam)

	// Get time-series data
	timeSeriesData, err := h.panelRead.GetLogVolumeTimeSeries(r.Context(), collectorName, appName, since, granularity)
	if err != nil {
		if err == panelread.ErrAppNotFound {
			http.Error(w, "App not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to fetch chart data", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"AppName":       appName,
		"CollectorName": collectorName,
		"TimeSeries":    timeSeriesData,
		"RangeParam":    rangeParam,
	}

	h.t.RenderHTTP(w, r, "app-logs-chart-data", data)
}

// getLogByID returns a single log entry by ID
func (h *handler) getLogByID(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	idStr := r.PathValue("id")

	// Check app access
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

	logEntry, err := h.panelRead.GetLogByID(r.Context(), collectorName, appName, id)
	if err != nil {
		if err == panelread.ErrAppNotFound {
			http.Error(w, "App not found", http.StatusNotFound)
			return
		}
		if err == panelread.ErrLogNotFound {
			http.Error(w, "Log not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to get log", http.StatusInternalServerError)
		return
	}

	// Redirect to group view with the specific log highlighted
	http.Redirect(w, r, fmt.Sprintf("/analytics/%s/apps/%s/logs?groupID=%d&logID=%d", collectorName, appName, logEntry.GroupID.Int64, logEntry.ID), http.StatusSeeOther)
}

func (h *handler) getAppAnalyticsData(ctx context.Context, collectorName, appName string) (*panelread.AppStats, any, error) {
	stats, grouped, err := h.panelRead.GetAppAnalytics(ctx, collectorName, appName)
	if err != nil {
		if err == panelread.ErrAppNotFound {
			return nil, nil, fmt.Errorf("app not found")
		}
		return nil, nil, err
	}
	return stats, grouped, nil
}

// parseTimeRange parses a time range string like "1h", "24h", "7d", "30d" and returns the time since
func parseTimeRange(rangeParam string) time.Time {
	if rangeParam == "" {
		return time.Now().Add(-24 * time.Hour) // default: 24h
	}

	// Match pattern: digits followed by 'h' or 'd'
	var value int
	var unit byte

	if len(rangeParam) < 2 {
		return time.Now().Add(-24 * time.Hour)
	}

	// Simple parsing: extract number and unit
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

// getBucketGranularity returns the bucket granularity parameter for the SQL query
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
		return "5min" // default: 24h
	}
}
