package panel

import (
	"embed"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	panelsvc "github.com/ai-shift/tracer/apps/panel/internal/services/panel"
	"github.com/ai-shift/tracer/apps/panel/web/middleware"
	"github.com/ai-shift/tracer/apps/panel/web/middleware/auth"
	"github.com/ai-shift/tracer/apps/panel/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t   templates.Templates
	svc *panelsvc.Service
}

func InitMux(svc *panelsvc.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:   templates.New(views),
		svc: svc,
	}

	// Dashboard
	m.HandleFunc("GET /", h.dashboard)
	m.HandleFunc("GET /collectors", h.dashboard)

	// Collector
	m.HandleFunc("GET /collectors/{collector}", h.getCollector)
	m.HandleFunc("GET /collectors/{collector}/resources", h.getCollectorResources)
	m.HandleFunc("GET /collectors/{collector}/apps", h.getCollectorApps)

	// App logs
	m.HandleFunc("GET /collectors/{collector}/apps/{app}", h.getApp)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs", h.getAppLogs)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups/{groupID}", h.getAppLogGroupByID)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/{id}", h.GetAppLogByID)

	return m
}

// Dashboard handlers

func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	collectors, err := h.svc.GetCollectorsView(r.Context(), user)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "dashboard", map[string]any{
		"Title":      "Panel",
		"User":       user,
		"Collectors": collectors,
		"Nav":        templates.NewNavBuilder().Add("panel", "panel").Build(),
	})
}

// Collector handlers

func (h *handler) getCollector(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	if collectorName == "" {
		h.t.RenderError(w, r, "Error", "Collector name required", http.StatusBadRequest)
		return
	}

	view, err := h.svc.GetCollectorDetail(r.Context(), user, collectorName)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to get collector", http.StatusInternalServerError)
		}
		return
	}

	h.t.RenderHTTP(w, r, "collector-overview", map[string]any{
		"CollectorName": collectorName,
		"User":          user,
		"Collector":     view,
		"Stats":         view.Stats,
		"StatsDisplay":  view.StatsDisplay,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(collectorName, collectorName).
			Build(),
	})
}

func (h *handler) getCollectorResources(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	if collectorName == "" {
		h.t.RenderError(w, r, "Error", "Collector name required", http.StatusBadRequest)
		return
	}
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	stats, err := h.svc.GetStatsHistory(r.Context(), user, collectorName, parseTimeRange(rangeParam))
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
		default:
			h.t.RenderError(w, r, "Error", "Failed to get stats history", http.StatusInternalServerError)
		}
		return
	}

	h.t.RenderHTTP(w, r, "collector-stats", map[string]any{
		"Title":         collectorName + " Resources",
		"CollectorName": collectorName,
		"RangeParam":    rangeParam,
		"Stats":         stats,
		"User":          user,
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(collectorName, collectorName).
			Add("resources", "resources").
			Build(),
	})
}

func (h *handler) getCollectorApps(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	if collectorName == "" {
		h.t.RenderError(w, r, "Error", "Collector name required", http.StatusBadRequest)
		return
	}

	view, err := h.svc.GetCollectorDetail(r.Context(), user, collectorName)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to get collector", http.StatusInternalServerError)
		}
		return
	}

	h.t.RenderHTTP(w, r, "collector-apps", map[string]any{
		"Title":         collectorName + " Apps",
		"CollectorName": collectorName,
		"Apps":          view.Apps,
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

// App logs handlers

func (h *handler) getApp(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	stats, grouped, err := h.svc.GetAppAnalytics(r.Context(), user, collectorName, appName)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to fetch analytics", http.StatusInternalServerError)
		}
		return
	}

	if middleware.IsMarkdownFormat(r) {
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
		"Stats":         stats,
		"Grouped":       grouped,
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

func (h *handler) getAppLogs(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.svc.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	level := r.URL.Query().Get("level")
	offset := parseOffset(r.URL.Query().Get("offset"))
	limit := int64(50)

	logGroups, err := h.svc.GetLogGroups(r.Context(), collectorName, appName, level, offset, limit)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrAppNotFound):
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to fetch log groups", http.StatusInternalServerError)
		}
		return
	}

	if middleware.IsMarkdownFormat(r) {
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
		"Groups":        logGroups.Groups,
		"Level":         level,
		"TotalCount":    logGroups.TotalCount,
		"ErrorCount":    logGroups.ErrorCount,
		"WarnCount":     logGroups.WarnCount,
		"InfoCount":     logGroups.InfoCount,
		"Offset":        offset,
		"Limit":         limit,
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

func (h *handler) getAppLogGroupByID(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
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

	hasAccess, err := h.svc.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	highlightLogID := parseOffset(r.URL.Query().Get("logID"))
	offset := parseOffset(r.URL.Query().Get("offset"))
	limit := int64(50)

	detail, err := h.svc.GetLogDetail(r.Context(), collectorName, appName, groupID, offset, limit)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrAppNotFound), errors.Is(err, panelsvc.ErrLogNotFound):
			h.t.RenderError(w, r, "Error", "Group not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
		}
		return
	}

	h.t.RenderHTTP(w, r, "app-logs-detail-page", map[string]any{
		"Title":          appName + " Log Details",
		"AppName":        appName,
		"CollectorName":  collectorName,
		"LogGroup":       detail.Group,
		"Logs":           detail.Logs,
		"Offset":         offset,
		"Limit":          limit,
		"HasMore":        offset+int64(len(detail.Logs)) < detail.TotalCount,
		"NextOffset":     offset + limit,
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

func (h *handler) GetAppLogByID(w http.ResponseWriter, r *http.Request) {
	user, err := auth.GetUser(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	idStr := r.PathValue("id")

	hasAccess, err := h.svc.CanAccessApp(r.Context(), user, appName)
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

	logEntry, err := h.svc.GetLogByID(r.Context(), collectorName, appName, id)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrAppNotFound):
			http.Error(w, "App not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrLogNotFound):
			http.Error(w, "Log not found", http.StatusNotFound)
		default:
			http.Error(w, "Failed to get log", http.StatusInternalServerError)
		}
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/panel/collectors/%s/apps/%s/logs/groups/%d?logID=%d", collectorName, appName, logEntry.GroupID.Int64, logEntry.ID), http.StatusSeeOther)
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
