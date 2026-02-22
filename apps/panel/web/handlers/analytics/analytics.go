package analytics

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-shift/tracer/apps/panel/internal/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/collectordb"
	"github.com/ai-shift/tracer/apps/panel/internal/collectors"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/web/htmx"
	"github.com/ai-shift/tracer/apps/panel/web/middleware"
	"github.com/ai-shift/tracer/apps/panel/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t                  templates.Templates
	dbManager          *dbmanager.Manager
	authService        *auth.Service
	collectorDBManager *collectordb.Manager
	tracker            *collectors.Tracker
}

func InitMux(dbManager *dbmanager.Manager, authService *auth.Service, collectorDBManager *collectordb.Manager, tracker *collectors.Tracker) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:                  templates.New(views),
		dbManager:          dbManager,
		authService:        authService,
		collectorDBManager: collectorDBManager,
		tracker:            tracker,
	}

	// Dashboard - list all collectors
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

	// Get collectors filtered by user access
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

// getCollectors returns collectors sections for HTMX refresh
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

// collectorOverview shows a single collector with stats and apps
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
		h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		return
	}

	// Get latest stats
	var stats *collectordb.SystemStat
	var statsDisplay *StatsDisplay
	s, err := h.collectorDBManager.GetLatestStats(r.Context(), collectorName)
	if err == nil && s != nil {
		stats = s
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
	stats, err := h.collectorDBManager.GetCollectorStatsHistory(r.Context(), collectorName, since)
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

	collector, exists := h.tracker.Get(collectorName)
	if !exists {
		h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		return
	}

	apps := h.filterAppsByAccess(r, user, collector.Apps)

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
	hasAccess, err := h.authService.UserHasAppAccess(r.Context(), user.ID, appName)
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
	*collectordb.SystemStat
	TimestampFormatted string // Formatted timestamp
	Staleness          string // "fresh", "warning", "stale", "old"
}

// appStats holds aggregated app-level counts
type appStats struct {
	Total    int64
	Errors   int64
	Warnings int64
	Infos    int64
}

// CollectorWithStats combines collector info with latest system stats
type CollectorWithStats struct {
	collectors.Collector
	Stats        *collectordb.SystemStat
	StatsDisplay *StatsDisplay
}

// formatStatsForDisplay creates a StatsDisplay from SystemStat
func formatStatsForDisplay(stats *collectordb.SystemStat) *StatsDisplay {
	if stats == nil {
		return nil
	}

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

	var userAppMap map[string]bool
	if user.IsAdmin != 1 {
		userApps, err := h.authService.GetUserApps(r.Context(), user.ID)
		if err != nil {
			return nil, err
		}
		userAppMap = make(map[string]bool)
		for _, app := range userApps {
			userAppMap[app] = true
		}
	}

	collectorsWithStats := make([]CollectorWithStats, 0, len(collectorList))
	for _, c := range collectorList {
		if user.IsAdmin != 1 {
			hasAccessibleApp := false
			for _, app := range c.Apps {
				if userAppMap[app] {
					hasAccessibleApp = true
					break
				}
			}
			if !hasAccessibleApp {
				continue
			}
		}

		cws := CollectorWithStats{
			Collector: c,
		}

		stats, err := h.collectorDBManager.GetLatestStats(r.Context(), c.Name)
		if err == nil && stats != nil {
			cws.Stats = stats
			cws.StatsDisplay = formatStatsForDisplay(stats)
		}

		collectorsWithStats = append(collectorsWithStats, cws)
	}

	return collectorsWithStats, nil
}

// userHasCollectorAccess checks if user can access the collector
func (h *handler) userHasCollectorAccess(r *http.Request, user *db.User, collectorName string) bool {
	if user.IsAdmin == 1 {
		return true
	}

	userApps, err := h.authService.GetUserApps(r.Context(), user.ID)
	if err != nil {
		return false
	}

	collector, exists := h.tracker.Get(collectorName)
	if !exists {
		return false
	}

	userAppMap := make(map[string]bool)
	for _, app := range userApps {
		userAppMap[app] = true
	}

	for _, app := range collector.Apps {
		if userAppMap[app] {
			return true
		}
	}

	return false
}

// filterAppsByAccess filters apps list by user access
func (h *handler) filterAppsByAccess(r *http.Request, user *db.User, apps []string) []string {
	if user.IsAdmin == 1 {
		return apps
	}

	userApps, err := h.authService.GetUserApps(r.Context(), user.ID)
	if err != nil {
		return nil
	}

	userAppMap := make(map[string]bool)
	for _, app := range userApps {
		userAppMap[app] = true
	}

	filtered := make([]string, 0)
	for _, app := range apps {
		if userAppMap[app] {
			filtered = append(filtered, app)
		}
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
	hasAccess, err := h.authService.UserHasAppAccess(r.Context(), user.ID, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	queries, err := h.dbManager.GetQueries(collectorName, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
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

		// Get group info
		logGroup, err := queries.GetLogGroupByID(r.Context(), groupID)
		if err != nil {
			h.t.RenderError(w, r, "Error", "Group not found", http.StatusNotFound)
			return
		}

		// Get logs for this group with pagination
		limit := int64(50)
		logs, err := queries.GetLogsByGroupID(r.Context(), db.GetLogsByGroupIDParams{
			GroupID: sql.NullInt64{Int64: groupID, Valid: true},
			Limit:   limit,
			Offset:  offset,
		})
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
			return
		}

		// Get total count
		totalCount, err := queries.CountLogsByGroupID(r.Context(), sql.NullInt64{Int64: groupID, Valid: true})
		if err != nil {
			slog.Error("Failed to count logs", "error", err)
			totalCount = 0
		}

		data := map[string]any{
			"Title":          appName + " Log Details",
			"AppName":        appName,
			"CollectorName":  collectorName,
			"LogGroup":       logGroup,
			"Logs":           logs,
			"User":           user,
			"IsAdmin":        user.IsAdmin == 1,
			"Nav":            nav,
			"Offset":         offset,
			"Limit":          limit,
			"HasMore":        offset+int64(len(logs)) < totalCount,
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

	// Fetch counts for each level
	totalCount, err := queries.CountLogGroups(r.Context())
	if err != nil {
		slog.Error("Failed to count log groups", "error", err)
		totalCount = 0
	}

	errorCount, err := queries.CountLogGroupsByLevel(r.Context(), "error")
	if err != nil {
		slog.Error("Failed to count error groups", "error", err)
		errorCount = 0
	}

	warnCount, err := queries.CountLogGroupsByLevel(r.Context(), "warn")
	if err != nil {
		slog.Error("Failed to count warn groups", "error", err)
		warnCount = 0
	}

	infoCount, err := queries.CountLogGroupsByLevel(r.Context(), "info")
	if err != nil {
		slog.Error("Failed to count info groups", "error", err)
		infoCount = 0
	}

	var groups any
	if level != "" {
		groups, err = queries.ListLogGroupsByLevel(r.Context(), db.ListLogGroupsByLevelParams{
			LOWER:  level,
			Limit:  limit,
			Offset: offset,
		})
	} else {
		groups, err = queries.ListLogGroups(r.Context(), db.ListLogGroupsParams{
			Limit:  limit,
			Offset: offset,
		})
	}
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to fetch log groups", http.StatusInternalServerError)
		return
	}

	// Determine current count for HasMore calculation
	var currentCount int64
	if level != "" {
		switch level {
		case "error":
			currentCount = errorCount
		case "warn":
			currentCount = warnCount
		case "info":
			currentCount = infoCount
		default:
			currentCount = totalCount
		}
	} else {
		currentCount = totalCount
	}

	data := map[string]any{
		"Title":         appName + " Logs",
		"AppName":       appName,
		"CollectorName": collectorName,
		"Groups":        groups,
		"Level":         level,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav":           nav,
		"TotalCount":    totalCount,
		"ErrorCount":    errorCount,
		"WarnCount":     warnCount,
		"InfoCount":     infoCount,
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
	hasAccess, err := h.authService.UserHasAppAccess(r.Context(), user.ID, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	queries, err := h.dbManager.GetQueries(collectorName, appName)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	since := parseTimeRange(rangeParam)
	granularity := getBucketGranularity(rangeParam)

	// Get time-series data
	timeSeriesData, err := queries.GetLogVolumeTimeSeries(r.Context(), db.GetLogVolumeTimeSeriesParams{
		Since:       sql.NullTime{Time: since, Valid: true},
		Granularity: granularity,
	})
	if err != nil {
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
	hasAccess, err := h.authService.UserHasAppAccess(r.Context(), user.ID, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	queries, err := h.dbManager.GetQueries(collectorName, appName)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid log ID", http.StatusBadRequest)
		return
	}

	logEntry, err := queries.GetLogByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Log not found", http.StatusNotFound)
			return
		}
		slog.Error("Failed to get log", "error", err)
		http.Error(w, "Failed to get log", http.StatusInternalServerError)
		return
	}

	// Redirect to group view with the specific log highlighted
	http.Redirect(w, r, fmt.Sprintf("/analytics/%s/apps/%s/logs?groupID=%d&logID=%d", collectorName, appName, logEntry.GroupID.Int64, logEntry.ID), http.StatusSeeOther)
}

func (h *handler) getAppAnalyticsData(ctx context.Context, collectorName, appName string) (*appStats, any, error) {
	queries, err := h.dbManager.GetQueries(collectorName, appName)
	if err != nil {
		return nil, nil, err
	}

	totalCount, err := queries.CountLogGroups(ctx)
	if err != nil {
		slog.Error("Failed to count log groups", "error", err)
		totalCount = 0
	}

	errorCount, err := queries.CountLogGroupsByLevel(ctx, "error")
	if err != nil {
		slog.Error("Failed to count error groups", "error", err)
		errorCount = 0
	}

	warnCount, err := queries.CountLogGroupsByLevel(ctx, "warn")
	if err != nil {
		slog.Error("Failed to count warn groups", "error", err)
		warnCount = 0
	}

	infoCount, err := queries.CountLogGroupsByLevel(ctx, "info")
	if err != nil {
		slog.Error("Failed to count info groups", "error", err)
		infoCount = 0
	}

	grouped, err := queries.ListLogGroups(ctx, db.ListLogGroupsParams{
		Limit:  20,
		Offset: 0,
	})
	if err != nil {
		return nil, nil, err
	}

	stats := &appStats{
		Total:    totalCount,
		Errors:   errorCount,
		Warnings: warnCount,
		Infos:    infoCount,
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
