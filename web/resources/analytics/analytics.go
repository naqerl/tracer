package analytics

import (
	"database/sql"
	"embed"
	"net/http"
	"time"

	"github.com/ai-shift/tracer/core/services/auth"
	"github.com/ai-shift/tracer/core/services/collectors"
	"github.com/ai-shift/tracer/core/services/dbmanager"
	"github.com/ai-shift/tracer/db"
	"github.com/ai-shift/tracer/web/htmx"
	"github.com/ai-shift/tracer/web/middleware"
	"github.com/ai-shift/tracer/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t           templates.Templates
	dbManager   *dbmanager.Manager
	authService *auth.Service
	tracker     *collectors.Tracker
}

func InitMux(dbManager *dbmanager.Manager, authService *auth.Service, tracker *collectors.Tracker) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:           templates.New(views),
		dbManager:   dbManager,
		authService: authService,
		tracker:     tracker,
	}

	// Dashboard - list all collectors
	m.HandleFunc("GET /", h.dashboard)
	m.HandleFunc("GET /collectors", h.getCollectors)

	// Collector routes
	m.HandleFunc("GET /{collector}", h.collectorOverview)
	m.HandleFunc("GET /{collector}/resources", h.collectorResources)
	m.HandleFunc("GET /{collector}/resources/data", h.collectorResourcesData)
	m.HandleFunc("GET /{collector}/apps", h.collectorApps)
	m.HandleFunc("GET /{collector}/apps/data", h.collectorAppsData)
	m.HandleFunc("GET /{collector}/apps/{app}", h.appAnalytics)
	m.HandleFunc("GET /{collector}/apps/{app}/logs", h.appLogs)

	return m
}

// dashboard shows all collectors overview
func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// Get collectors filtered by user access
	collectorsWithStats, err := h.getFilteredCollectors(r, user)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	nav := templates.NewNavBuilder().Add("analytics", "analytics").Build()

	data := map[string]any{
		"Title":      "Analytics",
		"User":       user,
		"IsAdmin":    user.IsAdmin == 1,
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

	data := map[string]any{
		"Collectors": collectorsWithStats,
	}

	h.t.RenderHTTP(w, r, "collectors-sections", data)
}

// collectorOverview shows a single collector with stats and apps
func (h *handler) collectorOverview(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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

	// Plain request: return skeleton only
	if !htmx.IsAjax(r) && !middleware.IsMarkdownFormat(r) {
		data := map[string]any{
			"Title":         collectorName,
			"CollectorName": collectorName,
			"User":          user,
			"IsAdmin":       user.IsAdmin == 1,
			"Nav":           nav,
		}
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
	var stats *db.SystemStat
	var statsDisplay *StatsDisplay
	s, err := h.authService.GetLatestStats(r.Context(), collectorName)
	if err == nil && s != nil {
		stats = s
		statsDisplay = formatStatsForDisplay(s)
	}

	// Filter apps by user access
	apps := h.filterAppsByAccess(r, user, collector.Apps)

	data := map[string]any{
		"Title":         collectorName,
		"CollectorName": collectorName,
		"IsConnected":   collector.IsConnected,
		"Stats":         stats,
		"StatsDisplay":  statsDisplay,
		"Apps":          apps,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav":           nav,
	}

	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "collector-overview-markdown", data)
	} else {
		h.t.RenderHTTP(w, r, "collector-overview-data", data)
	}
}

// collectorResources shows resource usage charts (was collectorStats)
func (h *handler) collectorResources(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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

	data := map[string]any{
		"Title":         collectorName + " Resources",
		"CollectorName": collectorName,
		"RangeParam":    rangeParam,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav":           nav,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "collector-stats-content", data)
	} else {
		h.t.RenderHTTP(w, r, "collector-stats", data)
	}
}

// collectorResourcesData returns lazy-loaded resource data (was collectorStatsData)
func (h *handler) collectorResourcesData(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	if !h.userHasCollectorAccess(r, user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	rangeParam := r.URL.Query().Get("range")
	var since time.Time

	switch rangeParam {
	case "1h":
		since = time.Now().Add(-1 * time.Hour)
	case "6h":
		since = time.Now().Add(-6 * time.Hour)
	case "7d":
		since = time.Now().Add(-7 * 24 * time.Hour)
	case "30d":
		since = time.Now().Add(-30 * 24 * time.Hour)
	default:
		since = time.Now().Add(-24 * time.Hour)
	}

	stats, err := h.authService.GetCollectorStatsHistory(r.Context(), collectorName, since)
	if err != nil {
		http.Error(w, "Failed to get stats history", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Stats": stats,
	}

	h.t.RenderHTTP(w, r, "collector-stats-data", data)
}

// collectorApps shows apps list for a collector
func (h *handler) collectorApps(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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

	data := map[string]any{
		"Title":         collectorName + " Apps",
		"CollectorName": collectorName,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav":           nav,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "collector-apps-content", data)
	} else {
		h.t.RenderHTTP(w, r, "collector-apps", data)
	}
}

// collectorAppsData returns lazy-loaded apps list
func (h *handler) collectorAppsData(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	if !h.userHasCollectorAccess(r, user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	collector, exists := h.tracker.Get(collectorName)
	if !exists {
		http.Error(w, "Collector not found", http.StatusNotFound)
		return
	}

	apps := h.filterAppsByAccess(r, user, collector.Apps)

	data := map[string]any{
		"CollectorName": collectorName,
		"Apps":          apps,
	}

	h.t.RenderHTTP(w, r, "collector-apps-data", data)
}

// appAnalytics shows analytics for a specific app
func (h *handler) appAnalytics(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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

	nav := templates.NewNavBuilder().
		Add("analytics", "analytics").
		Add(collectorName, collectorName).
		Add("apps", "apps").
		Add(appName, appName).
		Build()

	// Plain request: return skeleton only
	if !htmx.IsAjax(r) && !middleware.IsMarkdownFormat(r) {
		data := map[string]any{
			"Title":         appName,
			"AppName":       appName,
			"CollectorName": collectorName,
			"IsAdmin":       user.IsAdmin == 1,
			"Nav":           nav,
		}
		h.t.RenderHTTP(w, r, "app-analytics", data)
		return
	}

	// AJAX or Markdown: fetch data
	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		return
	}

	since := sql.NullTime{Time: time.Now().Add(-24 * time.Hour), Valid: true}
	stats, err := queries.GetPercentiles(r.Context(), since)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get stats", http.StatusInternalServerError)
		return
	}

	grouped, err := queries.CountLogsByMessage(r.Context(), 20)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to fetch grouped logs", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":         appName,
		"AppName":       appName,
		"CollectorName": collectorName,
		"Stats":         stats,
		"Grouped":       grouped,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav":           nav,
	}

	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "app-analytics-markdown", data)
	} else {
		h.t.RenderHTTP(w, r, "app-analytics-content", data)
	}
}

// Helper types and functions

// StatsDisplay holds formatted stats data for display
type StatsDisplay struct {
	*db.SystemStat
	TimestampFormatted string // Formatted timestamp
	Staleness          string // "fresh", "warning", "stale", "old"
}

// CollectorWithStats combines collector info with latest system stats
type CollectorWithStats struct {
	collectors.Collector
	Stats        *db.SystemStat
	StatsDisplay *StatsDisplay
}

// formatStatsForDisplay creates a StatsDisplay from SystemStat
func formatStatsForDisplay(stats *db.SystemStat) *StatsDisplay {
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

		stats, err := h.authService.GetLatestStats(r.Context(), c.Name)
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
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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

	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		return
	}

	level := r.URL.Query().Get("level")
	limit := int64(50)

	// Fetch stats (counts by level)
	since := sql.NullTime{Time: time.Now().Add(-24 * time.Hour), Valid: true}
	stats, err := queries.GetPercentiles(r.Context(), since)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get stats", http.StatusInternalServerError)
		return
	}

	// Fetch grouped logs
	var grouped []db.CountLogsByMessageRow
	if level != "" {
		groupedByLevel, err := queries.CountLogsByMessageAndLevel(r.Context(), db.CountLogsByMessageAndLevelParams{
			LOWER: level,
			Limit: limit,
		})
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
			return
		}
		for _, g := range groupedByLevel {
			grouped = append(grouped, db.CountLogsByMessageRow(g))
		}
	} else {
		grouped, err = queries.CountLogsByMessage(r.Context(), limit)
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
			return
		}
	}

	nav := templates.NewNavBuilder().
		Add("analytics", "analytics").
		Add(collectorName, collectorName).
		Add("apps", "apps").
		Add(appName, appName).
		Add("logs", "logs").
		Build()

	data := map[string]any{
		"Title":         appName + " Logs",
		"AppName":       appName,
		"CollectorName": collectorName,
		"Stats":         stats,
		"Grouped":       grouped,
		"Level":         level,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav":           nav,
	}

	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "app-logs-markdown", data)
	} else if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "app-logs-table", data)
	} else {
		h.t.RenderHTTP(w, r, "app-logs-page", data)
	}
}
