package panel

import (
        "embed"
        "errors"
        "fmt"
        "log/slog"
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

// LevelFilter represents a log level filter option
type LevelFilter struct {
        Val   string
        Label string
        Color string
}

var levelFilters = []LevelFilter{
        {Val: "", Label: "All", Color: "fg"},
        {Val: "error", Label: "Errors", Color: "red-500"},
        {Val: "warn", Label: "Warnings", Color: "yellow-500"},
        {Val: "info", Label: "Info", Color: "blue-500"},
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
        m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups", h.getLogGroups)
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
        })
}

func (h *handler) getCollectorResources(w http.ResponseWriter, r *http.Request) {
        user, err := auth.RequireOrRedirect(w, r)
        if err != nil {
                return
        }

        since := r.URL.Query().Get("since")
        if since == "" {
                since = "24h"
        }

        req := panelsvc.GetStatsHistoryRequest{
                User:          user,
                CollectorName: r.PathValue("collector"),
                Since:         parseSince(since),
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
                "Since":         since,
                "Stats":         stats,
                "User":          user,
                "IsAdmin":       user.IsAdmin,
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

        stats, err := h.svc.GetAppAnalytics(r.Context(), req)
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

        h.t.RenderHTTP(w, r, "app", map[string]any{
                "Title":     req.AppName + " Analytics",
                "App":       req.AppName,
                "Collector": req.CollectorName,
                "Stats":     stats,
                "IsAdmin":   user.IsAdmin,
        })
}

func parseSince(since string) time.Time {
        if since == "" {
                return time.Now().Add(-24 * time.Hour) // default to 24h
        }
        duration, err := time.ParseDuration(since)
        if err != nil {
                // Try parsing as days (e.g., "7d" -> "168h")
                if len(since) > 1 && since[len(since)-1] == 'd' {
                        days, err := strconv.Atoi(since[:len(since)-1])
                        if err == nil {
                                return time.Now().Add(-time.Duration(days) * 24 * time.Hour)
                        }
                }
                return time.Now().Add(-24 * time.Hour) // fallback
        }
        return time.Now().Add(-duration)
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
                Since:         parseSince(r.URL.Query().Get("since")),
                Offset:        parseOffset(r.URL.Query().Get("offset")),
                Limit:         50,
        }
        if err := req.Validate(); err != nil {
                h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
                return
        }

        groups, err := h.svc.GetLogGroups(r.Context(), req)
        if err != nil {
                slog.Error("GetLogGroups failed", "error", err, "collector", req.CollectorName, "app", req.AppName)
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

        h.t.RenderHTTP(w, r, "log-groups", map[string]any{
                "Groups":       groups,
                "Level":        req.Level,
                "Since":        r.URL.Query().Get("since"),
                "Collector":    req.CollectorName,
                "App":          req.AppName,
                "LevelFilters": levelFilters,
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

        h.t.RenderHTTP(w, r, "log-group", map[string]any{
                "Title":         req.AppName + " Log Details",
                "AppName":       req.AppName,
                "CollectorName": req.CollectorName,
                "LogGroup":      detail.Group,
                "Logs":          detail.Logs,
                "Offset":        req.Offset,
                "Limit":         req.Limit,
                "HasMore":       req.Offset+int64(len(detail.Logs)) < detail.TotalCount,
                "NextOffset":    req.Offset + req.Limit,
                "User":          user,
                "IsAdmin":       user.IsAdmin,
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

        h.t.RenderHTTP(w, r, "log", map[string]any{
                "Title":         fmt.Sprintf("Log #%d", logEntry.ID),
                "CollectorName": req.CollectorName,
                "AppName":       req.AppName,
                "Log":           logEntry,
                "User":          user,
                "IsAdmin":       user.IsAdmin,
        })
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
