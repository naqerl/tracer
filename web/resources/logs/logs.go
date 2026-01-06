package logs

import (
	"database/sql"
	"embed"
	"net/http"
	"strconv"
	"time"

	"github.com/ai-shift/tracer/core/services/auth"
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
}

func InitMux(dbManager *dbmanager.Manager, authService *auth.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:           templates.New(views),
		dbManager:   dbManager,
		authService: authService,
	}

	m.HandleFunc("GET /{app}", h.listLogs)
	m.HandleFunc("GET /{app}/embed", h.embedLogs)
	m.HandleFunc("GET /{app}/trace/{traceId}", h.showTrace)
	m.HandleFunc("GET /{app}/log/{id}", h.showLog)
	m.HandleFunc("GET /{app}/stats", h.getStats)

	return m
}

func (h *handler) checkAppAccess(w http.ResponseWriter, r *http.Request, appName string) bool {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return false
	}

	hasAccess, err := h.authService.UserHasAppAccess(r.Context(), user.ID, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return false
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return false
	}

	return true
}

func (h *handler) listLogs(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")

	if !h.checkAppAccess(w, r, appName) {
		return
	}

	level := r.URL.Query().Get("level")
	limitStr := r.URL.Query().Get("limit")

	limit := int64(100)
	if limitStr != "" {
		if l, err := strconv.ParseInt(limitStr, 10, 64); err == nil {
			limit = l
		}
	}

	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		return
	}

	var logs []db.Log
	if level != "" {
		logs, err = queries.ListLogsByLevel(r.Context(), db.ListLogsByLevelParams{
			Level: level,
			Limit: limit,
		})
	} else {
		logs, err = queries.ListRecentLogs(r.Context(), limit)
	}

	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"AppName": appName,
		"Logs":    logs,
		"Level":   level,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "logs-table", data)
	} else {
		h.t.RenderHTTP(w, r, "logs-page", data)
	}
}

func (h *handler) embedLogs(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")

	if !h.checkAppAccess(w, r, appName) {
		return
	}

	level := r.URL.Query().Get("level")
	limit := int64(50) // Fewer logs for embed view

	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		w.Write([]byte("App not found"))
		return
	}

	var logs []db.Log
	if level != "" {
		logs, err = queries.ListLogsByLevel(r.Context(), db.ListLogsByLevelParams{
			Level: level,
			Limit: limit,
		})
	} else {
		logs, err = queries.ListRecentLogs(r.Context(), limit)
	}

	if err != nil {
		w.Write([]byte("Failed to fetch logs"))
		return
	}

	data := map[string]any{
		"AppName": appName,
		"Logs":    logs,
		"Level":   level,
	}

	h.t.RenderHTTP(w, r, "logs-embed", data)
}

func (h *handler) showTrace(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")

	if !h.checkAppAccess(w, r, appName) {
		return
	}

	traceID := r.PathValue("traceId")

	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		return
	}

	logs, err := queries.FindLogsByTraceID(r.Context(), traceID)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to fetch trace", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"AppName": appName,
		"TraceID": traceID,
		"Logs":    logs,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "trace-content", data)
	} else {
		h.t.RenderHTTP(w, r, "trace-page", data)
	}
}

func (h *handler) showLog(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")

	if !h.checkAppAccess(w, r, appName) {
		return
	}

	idStr := r.PathValue("id")

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Invalid log ID", http.StatusBadRequest)
		return
	}

	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		return
	}

	log, err := queries.GetLogByID(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Log not found", http.StatusNotFound)
		return
	}

	data := map[string]any{
		"AppName": appName,
		"Log":     log,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "log-detail", data)
	} else {
		h.t.RenderHTTP(w, r, "log-page", data)
	}
}

func (h *handler) getStats(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")

	if !h.checkAppAccess(w, r, appName) {
		return
	}

	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	since := sql.NullTime{Time: time.Now().Add(-24 * time.Hour), Valid: true}
	stats, err := queries.GetPercentiles(r.Context(), since)
	if err != nil {
		http.Error(w, "Failed to get stats", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"AppName": appName,
		"Stats":   stats,
	}

	h.t.RenderHTTP(w, r, "stats-panel", data)
}
