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

	m.HandleFunc("GET /", h.dashboard)
	m.HandleFunc("GET /collectors/{collector}/stats", h.collectorStats)
	m.HandleFunc("GET /collectors", h.getCollectors)
	m.HandleFunc("GET /{app}", h.appAnalytics)

	return m
}

func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	allApps, err := h.dbManager.ListApps()
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list apps", http.StatusInternalServerError)
		return
	}

	// Filter apps based on user access
	var apps []string
	if user.IsAdmin == 1 {
		apps = allApps
	} else {
		userApps, err := h.authService.GetUserApps(r.Context(), user.ID)
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to get user apps", http.StatusInternalServerError)
			return
		}
		userAppMap := make(map[string]bool)
		for _, app := range userApps {
			userAppMap[app] = true
		}
		for _, app := range allApps {
			if userAppMap[app] {
				apps = append(apps, app)
			}
		}
	}

	// Get collectors filtered by user access
	collectorsWithStats, err := h.getFilteredCollectors(r, user)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":      "Analytics",
		"Apps":       apps,
		"User":       user,
		"IsAdmin":    user.IsAdmin == 1,
		"Nav":        templates.NewNavData("Analytics", "analytics", user.Username),
		"Collectors": collectorsWithStats,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "dashboard-content", data)
	} else {
		h.t.RenderHTTP(w, r, "dashboard", data)
	}
}

func (h *handler) appAnalytics(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

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

	since := sql.NullTime{Time: time.Now().Add(-24 * time.Hour), Valid: true}

	stats, err := queries.GetPercentiles(r.Context(), since)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get stats", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"AppName": appName,
		"Stats":   stats,
		"IsAdmin": user.IsAdmin == 1,
		"Nav":     templates.NewNavData(appName, "analytics", user.Username),
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "app-analytics-content", data)
	} else {
		h.t.RenderHTTP(w, r, "app-analytics", data)
	}
}

func (h *handler) getCollectors(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Get collectors filtered by user access
	collectorsWithStats, err := h.getFilteredCollectors(r, user)
	if err != nil {
		http.Error(w, "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Collectors": collectorsWithStats,
	}

	// Render collector sections for HTMX refresh
	h.t.RenderHTTP(w, r, "collectors-sections", data)
}

// CollectorWithStats combines collector info with latest system stats
type CollectorWithStats struct {
	collectors.Collector
	Stats *db.SystemStat
}

func (h *handler) collectorStats(w http.ResponseWriter, r *http.Request) {
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

	// Check if user has access to at least one app from this collector
	if user.IsAdmin != 1 {
		userApps, err := h.authService.GetUserApps(r.Context(), user.ID)
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to get user apps", http.StatusInternalServerError)
			return
		}

		// Get collector's apps
		collector, exists := h.tracker.Get(collectorName)
		if !exists {
			h.t.RenderError(w, r, "Not Found", "Collector not found", http.StatusNotFound)
			return
		}

		// Check if user has access to any of the collector's apps
		userAppMap := make(map[string]bool)
		for _, app := range userApps {
			userAppMap[app] = true
		}

		hasAccess := false
		for _, app := range collector.Apps {
			if userAppMap[app] {
				hasAccess = true
				break
			}
		}

		if !hasAccess {
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
			return
		}
	}

	// Parse time range parameter (default: 24h)
	rangeParam := r.URL.Query().Get("range")
	var since time.Time
	var rangeLabel string

	switch rangeParam {
	case "1h":
		since = time.Now().Add(-1 * time.Hour)
		rangeLabel = "Last 1 Hour"
	case "6h":
		since = time.Now().Add(-6 * time.Hour)
		rangeLabel = "Last 6 Hours"
	case "7d":
		since = time.Now().Add(-7 * 24 * time.Hour)
		rangeLabel = "Last 7 Days"
	case "30d":
		since = time.Now().Add(-30 * 24 * time.Hour)
		rangeLabel = "Last 30 Days"
	default: // "24h" or empty
		since = time.Now().Add(-24 * time.Hour)
		rangeLabel = "Last 24 Hours"
		rangeParam = "24h"
	}

	// Get stats history
	stats, err := h.authService.GetCollectorStatsHistory(r.Context(), collectorName, since)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get stats history", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":         collectorName + " Stats",
		"CollectorName": collectorName,
		"Stats":         stats,
		"RangeParam":    rangeParam,
		"RangeLabel":    rangeLabel,
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav":           templates.NewNavData("Analytics", "analytics", user.Username),
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "collector-stats-content", data)
	} else {
		h.t.RenderHTTP(w, r, "collector-stats", data)
	}
}

// getFilteredCollectors returns collectors filtered by user access
// Admins see all collectors, regular users only see collectors that serve apps they have access to
func (h *handler) getFilteredCollectors(r *http.Request, user *db.User) ([]CollectorWithStats, error) {
	collectorList := h.tracker.List()

	// Get user's accessible apps (for non-admins)
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
		// For non-admins, check if collector has any apps the user can access
		if user.IsAdmin != 1 {
			hasAccessibleApp := false
			for _, app := range c.Apps {
				if userAppMap[app] {
					hasAccessibleApp = true
					break
				}
			}
			if !hasAccessibleApp {
				continue // Skip this collector
			}
		}

		cws := CollectorWithStats{
			Collector: c,
		}

		// Try to get latest stats for this collector
		stats, err := h.authService.GetLatestStats(r.Context(), c.Name)
		if err == nil && stats != nil {
			cws.Stats = stats
		}

		collectorsWithStats = append(collectorsWithStats, cws)
	}

	return collectorsWithStats, nil
}
