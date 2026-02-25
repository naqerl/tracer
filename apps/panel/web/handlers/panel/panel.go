package panel

import (
	"embed"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	panelsvc "tracer/apps/panel/internal/services/panel"
	"tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/templates"
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
	m.HandleFunc("GET /", h.getDashboard)
	m.HandleFunc("GET /collectors", h.getDashboard)

	// Collector
	m.HandleFunc("GET /collectors/{collector}", h.getCollector)
	m.HandleFunc("GET /collectors/{collector}/resources", h.getCollectorResources)
	m.HandleFunc("GET /collectors/{collector}/apps", h.getCollectorApps)

	// App logs
	m.HandleFunc("GET /collectors/{collector}/apps/{app}", h.getApp)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs", h.getLogGroups)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups/{groupID}", h.getLogGroupByID)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/{id}", h.getLogByID)

	return m
}

// Dashboard handlers

func (h *handler) getDashboard(w http.ResponseWriter, r *http.Request) {
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

	req := panelsvc.GetCollectorDetailRequest{
		User:          user,
		CollectorName: r.PathValue("collector"),
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	view, err := h.svc.GetCollectorDetail(r.Context(), req)
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
		"CollectorName": req.CollectorName,
		"User":          user,
		"Collector":     view,
		"Stats":         view.Stats,
		"StatsDisplay":  view.StatsDisplay,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(req.CollectorName, req.CollectorName).
			Build(),
	})
}

func (h *handler) getCollectorResources(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	req := panelsvc.GetStatsHistoryRequest{
		User:          user,
		CollectorName: r.PathValue("collector"),
		Since:         parseTimeRange(rangeParam),
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	stats, err := h.svc.GetStatsHistory(r.Context(), req)
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
		"Title":         req.CollectorName + " Resources",
		"CollectorName": req.CollectorName,
		"RangeParam":    rangeParam,
		"Stats":         stats,
		"User":          user,
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(req.CollectorName, req.CollectorName).
			Add("resources", "resources").
			Build(),
	})
}

func (h *handler) getCollectorApps(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	req := panelsvc.GetCollectorDetailRequest{
		User:          user,
		CollectorName: r.PathValue("collector"),
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", "Collector name required", http.StatusBadRequest)
		return
	}

	view, err := h.svc.GetCollectorDetail(r.Context(), req)
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
		"Title":         req.CollectorName + " Apps",
		"CollectorName": req.CollectorName,
		"Apps":          view.Apps,
		"User":          user,
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(req.CollectorName, req.CollectorName).
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

	req := panelsvc.GetAppAnalyticsRequest{
		User:          user,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	stats, grouped, err := h.svc.GetAppAnalytics(r.Context(), req)
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

	h.t.RenderHTTP(w, r, "app-page", map[string]any{
		"Title":         req.AppName + " Analytics",
		"AppName":       req.AppName,
		"CollectorName": req.CollectorName,
		"Stats":         stats,
		"Grouped":       grouped,
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(req.CollectorName, req.CollectorName).
			Add("apps", "apps").
			Add(req.AppName, req.AppName).
			Build(),
	})
}

func (h *handler) getLogGroups(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	req := panelsvc.GetLogGroupsRequest{
		User:          user,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		Level:         r.URL.Query().Get("level"),
		Offset:        parseOffset(r.URL.Query().Get("offset")),
		Limit:         50,
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	logGroups, err := h.svc.GetLogGroups(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to fetch log groups", http.StatusInternalServerError)
		}
		return
	}

	h.t.RenderHTTP(w, r, "app-logs-page", map[string]any{
		"Title":         req.AppName + " Logs",
		"AppName":       req.AppName,
		"CollectorName": req.CollectorName,
		"Groups":        logGroups.Groups,
		"Level":         req.Level,
		"TotalCount":    logGroups.TotalCount,
		"ErrorCount":    logGroups.ErrorCount,
		"WarnCount":     logGroups.WarnCount,
		"InfoCount":     logGroups.InfoCount,
		"Offset":        req.Offset,
		"Limit":         req.Limit,
		"User":          user,
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(req.CollectorName, req.CollectorName).
			Add("apps", "apps").
			Add(req.AppName, req.AppName).
			Add("logs", "logs").
			Build(),
	})
}

func (h *handler) getLogGroupByID(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
		return
	}

	highlightLogID := parseOffset(r.URL.Query().Get("logID"))

	req := panelsvc.GetLogDetailRequest{
		User:          user,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		GroupID:       groupID,
		Offset:        parseOffset(r.URL.Query().Get("offset")),
		Limit:         50,
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	detail, err := h.svc.GetLogDetail(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound), errors.Is(err, panelsvc.ErrLogNotFound):
			h.t.RenderError(w, r, "Error", "Group not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
		}
		return
	}

	h.t.RenderHTTP(w, r, "app-logs-detail-page", map[string]any{
		"Title":          req.AppName + " Log Details",
		"AppName":        req.AppName,
		"CollectorName":  req.CollectorName,
		"LogGroup":       detail.Group,
		"Logs":           detail.Logs,
		"Offset":         req.Offset,
		"Limit":          req.Limit,
		"HasMore":        req.Offset+int64(len(detail.Logs)) < detail.TotalCount,
		"NextOffset":     req.Offset + req.Limit,
		"HighlightLogID": highlightLogID,
		"User":           user,
		"IsAdmin":        user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(req.CollectorName, req.CollectorName).
			Add("apps", "apps").
			Add(req.AppName, req.AppName).
			Add("logs", "logs").
			Build(),
	})
}

func (h *handler) getLogByID(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Invalid log ID", http.StatusBadRequest)
		return
	}

	req := panelsvc.GetLogByIDRequest{
		User:          user,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		ID:            id,
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	logEntry, err := h.svc.GetLogByID(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrLogNotFound):
			h.t.RenderError(w, r, "Error", "Log not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to get log", http.StatusInternalServerError)
		}
		return
	}

	h.t.RenderHTTP(w, r, "app-log-page", map[string]any{
		"Title":         fmt.Sprintf("Log #%d", logEntry.ID),
		"CollectorName": req.CollectorName,
		"AppName":       req.AppName,
		"Log":           logEntry,
		"User":          user,
		"IsAdmin":       user.IsAdmin,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add("collectors", "collectors").
			Add(req.CollectorName, req.CollectorName).
			Add("apps", "apps").
			Add(req.AppName, req.AppName).
			Add("logs", "logs").
			Add("log", "log").
			Build(),
	})
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
