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

	m.HandleFunc("GET /{app}", h.listLogsGrouped)        // Default to grouped view
	m.HandleFunc("GET /{app}/all", h.listLogs)           // All logs (ungrouped)
	m.HandleFunc("GET /{app}/embed", h.embedLogsGrouped) // Embedded grouped view
	m.HandleFunc("GET /{app}/trace/{traceId}", h.showTrace)
	m.HandleFunc("GET /{app}/log/{id}", h.showLog)
	m.HandleFunc("GET /{app}/message", h.showMessageLogs) // Logs by message
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
	user := middleware.GetUser(r)

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
		"Nav":     templates.NewNavData("Logs: "+appName, "analytics", user.Username),
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
	user := middleware.GetUser(r)

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
		"Nav":     templates.NewNavData("Trace View", "analytics", user.Username),
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "trace-content", data)
	} else {
		h.t.RenderHTTP(w, r, "trace-page", data)
	}
}

func (h *handler) showLog(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")
	user := middleware.GetUser(r)

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
		"Nav":     templates.NewNavData("Log Detail", "analytics", user.Username),
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

// listLogsGrouped shows logs grouped by message (most frequent first)
func (h *handler) listLogsGrouped(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")
	user := middleware.GetUser(r)

	if !h.checkAppAccess(w, r, appName) {
		return
	}

	level := r.URL.Query().Get("level")
	limit := int64(100)

	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		return
	}

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
		// Convert to common type
		for _, g := range groupedByLevel {
			grouped = append(grouped, db.CountLogsByMessageRow{
				Level:    g.Level,
				Message:  g.Message,
				Count:    g.Count,
				LatestAt: g.LatestAt,
				SampleID: g.SampleID,
			})
		}
	} else {
		grouped, err = queries.CountLogsByMessage(r.Context(), limit)
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
			return
		}
	}

	data := map[string]any{
		"AppName": appName,
		"Grouped": grouped,
		"Level":   level,
		"Nav":     templates.NewNavData("Logs: "+appName, "analytics", user.Username),
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "logs-grouped-table", data)
	} else {
		h.t.RenderHTTP(w, r, "logs-grouped-page", data)
	}
}

// embedLogsGrouped shows grouped logs for embedding in analytics page
func (h *handler) embedLogsGrouped(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")

	if !h.checkAppAccess(w, r, appName) {
		return
	}

	level := r.URL.Query().Get("level")
	limit := int64(50) // Fewer for embed view

	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		w.Write([]byte("App not found"))
		return
	}

	var grouped []db.CountLogsByMessageRow
	if level != "" {
		groupedByLevel, err := queries.CountLogsByMessageAndLevel(r.Context(), db.CountLogsByMessageAndLevelParams{
			LOWER: level,
			Limit: limit,
		})
		if err != nil {
			w.Write([]byte("Failed to fetch logs"))
			return
		}
		for _, g := range groupedByLevel {
			grouped = append(grouped, db.CountLogsByMessageRow{
				Level:    g.Level,
				Message:  g.Message,
				Count:    g.Count,
				LatestAt: g.LatestAt,
				SampleID: g.SampleID,
			})
		}
	} else {
		grouped, err = queries.CountLogsByMessage(r.Context(), limit)
		if err != nil {
			w.Write([]byte("Failed to fetch logs"))
			return
		}
	}

	data := map[string]any{
		"AppName": appName,
		"Grouped": grouped,
		"Level":   level,
	}

	h.t.RenderHTTP(w, r, "logs-grouped-embed", data)
}

// showMessageLogs shows all logs with a specific level and message
func (h *handler) showMessageLogs(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")
	user := middleware.GetUser(r)

	if !h.checkAppAccess(w, r, appName) {
		return
	}

	level := r.URL.Query().Get("level")
	message := r.URL.Query().Get("message")

	if level == "" || message == "" {
		h.t.RenderError(w, r, "Error", "Level and message are required", http.StatusBadRequest)
		return
	}

	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		return
	}

	logs, err := queries.GetLogsByLevelAndMessage(r.Context(), db.GetLogsByLevelAndMessageParams{
		LOWER:   level,
		Message: message,
		Limit:   500, // Reasonable limit for detail view
	})
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"AppName": appName,
		"Logs":    logs,
		"Level":   level,
		"Message": message,
		"Count":   len(logs),
		"Nav":     templates.NewNavData("Message Logs", "analytics", user.Username),
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "message-logs-content", data)
	} else {
		h.t.RenderHTTP(w, r, "message-logs-page", data)
	}
}
