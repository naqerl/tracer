package analytics

import (
	"database/sql"
	"embed"
	"net/http"
	"time"

	"github.com/ai-shift/tracer/core/services/dbmanager"
	"github.com/ai-shift/tracer/web/htmx"
	"github.com/ai-shift/tracer/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t         templates.Templates
	dbManager *dbmanager.Manager
}

func InitMux(dbManager *dbmanager.Manager) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:         templates.New(views),
		dbManager: dbManager,
	}

	m.HandleFunc("GET /", h.dashboard)
	m.HandleFunc("GET /{app}", h.appAnalytics)
	m.HandleFunc("GET /{app}/chart", h.getChart)

	return m
}

func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	apps, err := h.dbManager.ListApps()
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list apps", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title": "Tracer Dashboard",
		"Apps":  apps,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "dashboard-content", data)
	} else {
		h.t.RenderHTTP(w, r, "dashboard", data)
	}
}

func (h *handler) appAnalytics(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")

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

	hourly, err := queries.CountByLevelAndHour(r.Context(), since)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get hourly stats", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"AppName": appName,
		"Stats":   stats,
		"Hourly":  hourly,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "app-analytics-content", data)
	} else {
		h.t.RenderHTTP(w, r, "app-analytics", data)
	}
}

func (h *handler) getChart(w http.ResponseWriter, r *http.Request) {
	appName := r.PathValue("app")

	queries, err := h.dbManager.GetQueries(appName)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	since := sql.NullTime{Time: time.Now().Add(-24 * time.Hour), Valid: true}
	hourly, err := queries.CountByLevelAndHour(r.Context(), since)
	if err != nil {
		http.Error(w, "Failed to get data", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"AppName": appName,
		"Hourly":  hourly,
	}

	h.t.RenderHTTP(w, r, "chart", data)
}
