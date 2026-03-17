package panel

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"math"
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

const (
	sinceCookieName   = "tracer_panel_since"
	defaultSinceLabel = "24h"
)

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
	m.HandleFunc("GET /collectors/{collector}/apps", h.getCollectorApps)
	m.HandleFunc("GET /collectors/{collector}/resources", h.getCollectorResources)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/uptime/percentage", h.getDashboardAppHealth)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/errors/count", h.getDashboardAppErrorCount)

	// App logs
	m.HandleFunc("GET /collectors/{collector}/apps/{app}", h.getApp)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups", h.getLogGroups)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups/{groupID}", h.getLogGroupByID)
	m.HandleFunc("DELETE /collectors/{collector}/apps/{app}/logs/groups/{groupID}", h.deleteLogGroup)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/{id}", h.getLogByID)

	return m
}

// Dashboard handlers

func (h *handler) getDashboard(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	sinceLabel := ensureSinceCookie(w, r)
	collectors, err := h.svc.ListCollectors(r.Context(), userID)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "dashboard", map[string]any{
		"Title":      "Panel",
		"UserID":     userID,
		"Collectors": dashboardCollectorsView(collectors),
		"Since":      sinceLabel,
	})
}

// Collector handlers

func (h *handler) getCollector(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	req := panelsvc.GetCollectorRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	view, err := h.svc.GetCollector(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
		default:
			h.t.RenderError(w, r, "Error", "Failed to get collector", http.StatusInternalServerError)
		}
		return
	}

	sinceLabel := ensureSinceCookie(w, r)
	h.t.RenderHTTP(w, r, "collector-overview", map[string]any{
		"CollectorName": req.CollectorName,
		"Collector":     view,
		"Stats":         view.Stats,
		"Since":         sinceLabel,
	})
}

func (h *handler) getCollectorApps(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}
	ensureSinceCookie(w, r)

	req := panelsvc.ListCollectorAppsRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}
	apps, err := h.svc.ListCollectorApps(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderHTTP(w, r, "collector-apps", map[string]any{
				"CollectorName": req.CollectorName,
				"Apps":          []appView{},
			})
		default:
			h.t.RenderError(w, r, "Error", "Failed to get collector apps", http.StatusInternalServerError)
		}
		return
	}

	h.t.RenderHTTP(w, r, "collector-apps", map[string]any{
		"CollectorName": req.CollectorName,
		"Apps":          collectorAppsView(apps),
	})
}

func (h *handler) getCollectorResources(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	sinceLabel, since := readSince(w, r)

	req := panelsvc.GetStatsHistoryRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
		Since:         since,
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	stats, err := h.svc.GetStatsHistory(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
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
		"Since":         sinceLabel,
		"Stats":         stats,
	})
}

func (h *handler) getDashboardAppHealth(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	_, since := readSince(w, r)

	req := panelsvc.GetAppHealthRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		Since:         since,
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	health, err := h.svc.GetAppHealth(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to fetch app health", http.StatusInternalServerError)
		}
		return
	}

	h.t.RenderHTTP(w, r, "app-health", map[string]any{
		"Health": appHealthViewFromService(health),
	})
}

func (h *handler) getDashboardAppErrorCount(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	_, since := readSince(w, r)

	req := panelsvc.GetAppErrorCountRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		Since:         since,
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	count, err := h.svc.GetAppErrorCount(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to fetch app error count", http.StatusInternalServerError)
		}
		return
	}

	h.t.RenderHTTP(w, r, "app-error-count", map[string]any{
		"Errors": appErrorCountView{
			Count: count,
			Class: appErrorCountClass(count),
		},
	})
}

// App logs handlers

func (h *handler) getApp(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	req := panelsvc.GetAppStatsRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	stats, err := h.svc.GetAppStats(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to fetch analytics", http.StatusInternalServerError)
		}
		return
	}

	sinceLabel, since := readSince(w, r)
	healthReq := panelsvc.GetAppHealthRequest{
		UserID:        userID,
		CollectorName: req.CollectorName,
		AppName:       req.AppName,
		Since:         since,
	}
	if err := healthReq.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	uptime, err := h.svc.GetAppHealth(r.Context(), healthReq)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			h.t.RenderError(w, r, "Error", "Failed to fetch uptime", http.StatusInternalServerError)
		}
		return
	}
	h.t.RenderHTTP(w, r, "app", map[string]any{
		"Title":     req.AppName + " Analytics",
		"App":       req.AppName,
		"Collector": req.CollectorName,
		"Stats":     stats,
		"Since":     sinceLabel,
		"Uptime":    appHealthViewFromService(uptime),
	})
}

func readSince(w http.ResponseWriter, r *http.Request) (string, time.Time) {
	label := ensureSinceCookie(w, r)
	return label, parseSinceLabel(label)
}

func ensureSinceCookie(w http.ResponseWriter, r *http.Request) string {
	cookie, err := r.Cookie(sinceCookieName)
	if err == nil {
		label := sanitizeSinceLabel(cookie.Value)
		if label == cookie.Value {
			return label
		}
	}
	setSinceCookie(w, r, defaultSinceLabel)
	return defaultSinceLabel
}

func setSinceCookie(w http.ResponseWriter, r *http.Request, label string) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     sinceCookieName,
		Value:    label,
		Path:     "/",
		MaxAge:   31536000,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func parseSinceLabel(label string) time.Time {
	label = sanitizeSinceLabel(label)
	duration, err := time.ParseDuration(label)
	if err != nil {
		if len(label) > 1 && label[len(label)-1] == 'd' {
			days, err := strconv.Atoi(label[:len(label)-1])
			if err == nil {
				return time.Now().Add(-time.Duration(days) * 24 * time.Hour)
			}
		}
		return time.Now().Add(-24 * time.Hour)
	}
	return time.Now().Add(-duration)
}

func sanitizeSinceLabel(label string) string {
	switch label {
	case "1h", "6h", "24h", "7d", "30d":
		return label
	default:
		return defaultSinceLabel
	}
}

type uptimeBarView struct {
	Class string
	Title string
}

type appHealthView struct {
	UptimeText      string
	UptimeTextClass string
	Bars            []uptimeBarView
}

type appErrorCountView struct {
	Count int64
	Class string
}

type logPageView struct {
	ID             int64
	Level          string
	Message        string
	Context        any
	CreatedAt      time.Time
	TraceIDDisplay string
	GroupID        int64
}

type appView struct {
	Name   string
	Health appHealthView
}

type collectorView struct {
	Name        string
	RemoteAddr  string
	IsConnected bool
	ConnectedAt time.Time
	LastSeenAt  time.Time
	Stats       *panelsvc.CollectorStats
}

func dashboardCollectorsView(collectors []panelsvc.Collector) []collectorView {
	result := make([]collectorView, 0, len(collectors))
	for _, collector := range collectors {
		result = append(result, collectorView{
			Name:        collector.Name,
			RemoteAddr:  collector.RemoteAddr,
			IsConnected: collector.IsConnected,
			ConnectedAt: collector.ConnectedAt,
			LastSeenAt:  collector.LastSeenAt,
			Stats:       collector.Stats,
		})
	}
	return result
}

func collectorAppsView(apps []string) []appView {
	result := make([]appView, 0, len(apps))
	for _, app := range apps {
		result = append(result, appView{
			Name: app,
		})
	}
	return result
}

func appHealthViewFromService(health panelsvc.AppHealth) appHealthView {
	return appHealthView{
		UptimeText:      uptimePercentText(health.UptimePercent, health.PeriodCount),
		UptimeTextClass: uptimePercentClass(health.UptimePercent, health.PeriodCount),
		Bars:            uptimeBarsView(health.Buckets),
	}
}

func appErrorCountClass(v int64) string {
	if v > 0 {
		return "text-red-400"
	}
	return "text-fg-secondary"
}

func uptimeBarsView(buckets []panelsvc.HealthBucket) []uptimeBarView {
	result := make([]uptimeBarView, 0, len(buckets))
	for _, bucket := range buckets {
		status := uptimeBucketStatus(bucket.UpDuration, bucket.DownDuration)
		result = append(result, uptimeBarView{
			Class: uptimeBarClass(status),
			Title: uptimeBucketTitle(bucket.Start, bucket.End, bucket.UpDuration, bucket.DownDuration),
		})
	}
	return result
}

func uptimePercentText(v float64, periodCount int) string {
	if periodCount == 0 {
		return "N/A"
	}
	if v >= 100 {
		return "100"
	}
	return fmt.Sprintf("%.0f", math.Floor(v))
}

func uptimePercentClass(v float64, periodCount int) string {
	if periodCount == 0 {
		return "text-fg-secondary"
	}
	if v < 95 {
		return "text-red-400"
	}
	if v < 99 {
		return "text-yellow-400"
	}
	return "text-fg-secondary"
}

func uptimeBarClass(status string) string {
	switch status {
	case "up":
		return "bg-green-500 border border-transparent"
	case "down":
		return "bg-red-500 border border-transparent"
	case "mixed":
		return "bg-yellow-500 border border-transparent"
	default:
		return "bg-bg-secondary border border-bg-secondary"
	}
}

func uptimeBucketStatus(upDuration, downDuration time.Duration) string {
	switch {
	case upDuration == 0 && downDuration == 0:
		return "unknown"
	case downDuration == 0:
		return "up"
	case upDuration == 0:
		return "down"
	default:
		return "mixed"
	}
}

func uptimeBucketTitle(start, end time.Time, upDuration, downDuration time.Duration) string {
	title := fmt.Sprintf("%s - %s", start.Format("2006-01-02 15:04"), end.Format("15:04"))
	total := upDuration + downDuration
	if total == 0 {
		return title + " • no uptime data"
	}
	return fmt.Sprintf("%s • %.2f%% uptime", title, (float64(upDuration)/float64(total))*100)
}

func (h *handler) getLogGroups(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	_, since := readSince(w, r)
	req := panelsvc.GetLogGroupsRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		Level:         r.URL.Query().Get("level"),
		Since:         since,
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
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
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
		"Collector":    req.CollectorName,
		"App":          req.AppName,
		"LevelFilters": levelFilters,
	})
}

func (h *handler) getLogGroupByID(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}
	sinceLabel, since := readSince(w, r)
	now := time.Now()

	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
		return
	}

	req := panelsvc.GetLogDetailRequest{
		UserID:        userID,
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
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
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
		"Since":         sinceLabel,
		"SinceStart":    since.Unix(),
		"SinceEnd":      now.Unix(),
		"LogGroup":      detail.Group,
		"Logs":          detail.Logs,
		"Offset":        req.Offset,
		"Limit":         req.Limit,
		"HasMore":       req.Offset+int64(len(detail.Logs)) < detail.TotalCount,
		"NextOffset":    req.Offset + req.Limit,
	})
}

func (h *handler) deleteLogGroup(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
		return
	}

	req := panelsvc.DeleteLogGroupRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		GroupID:       groupID,
	}
	if err := req.Validate(); err != nil {
		h.t.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.svc.DeleteLogGroup(r.Context(), req); err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound), errors.Is(err, panelsvc.ErrLogNotFound):
			h.t.RenderError(w, r, "Error", "Group not found", http.StatusNotFound)
		default:
			slog.Error("Failed to delete log group", "error", err, "groupID", req.GroupID, "collector", req.CollectorName, "app", req.AppName)
			h.t.RenderError(w, r, "Error", "Failed to delete log group", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("HX-Redirect", fmt.Sprintf("/panel/collectors/%s/apps/%s", req.CollectorName, req.AppName))
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getLogByID(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}
	ensureSinceCookie(w, r)

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Invalid log ID", http.StatusBadRequest)
		return
	}

	req := panelsvc.GetLogByIDRequest{
		UserID:        userID,
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
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			h.t.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
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
		"Log": logPageView{
			ID:             logEntry.ID,
			Level:          logEntry.Level,
			Message:        logEntry.Message,
			Context:        logEntry.Context,
			CreatedAt:      logEntry.CreatedAt,
			TraceIDDisplay: logTraceIDDisplay(logEntry.TraceID),
			GroupID:        logEntry.GroupID.Int64,
		},
	})
}

func logTraceIDDisplay(traceID sql.NullString) string {
	if !traceID.Valid {
		return "Not present"
	}
	return traceID.String
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
