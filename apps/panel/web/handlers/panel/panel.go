package panel

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	panelsvc "tracer/apps/panel/internal/services/panel"
	updatesvc "tracer/apps/panel/internal/services/update"
	"tracer/apps/panel/web/handlers/panel/view"
	"tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/templates"
)

//go:embed views/log-group-plot.js
var logGroupPlotJS []byte

type handler struct {
	svc       *panelsvc.Service
	updateSvc *updatesvc.Service
}

// LevelFilter represents a log level filter option
type LevelFilter struct {
	Val   string
	Label string
	Color string
}

var levelFilters = []LevelFilter{
	{Val: "all", Label: "All", Color: "fg"},
	{Val: "error", Label: "Errors", Color: "red-500"},
	{Val: "warn", Label: "Warnings", Color: "yellow-500"},
	{Val: "info", Label: "Info", Color: "blue-500"},
}

const defaultLogLevel = "error"

const (
	sinceCookieName   = "tracer_panel_since"
	defaultSinceLabel = "24h"
)

func InitMux(svc *panelsvc.Service, updateSvc *updatesvc.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		svc:       svc,
		updateSvc: updateSvc,
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
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/stats", h.getAppStats)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups", h.getLogGroups)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups/{groupID}", h.getLogGroupByID)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups/{groupID}/entries", h.getLogGroupEntries)
	m.HandleFunc("DELETE /collectors/{collector}/apps/{app}/logs/groups/{groupID}", h.deleteLogGroup)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/{id}", h.getLogByID)

	// Update — single endpoint that triggers panel self-update AND sends
	// update commands to all connected collectors.
	m.HandleFunc("POST /update", h.postUpdate)

	// Static assets
	m.HandleFunc("GET /logs/groups/plot", h.getLogGroupPlot)

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
		templates.RenderError(w, r, "Error", "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	view.Dashboard(view.DashboardProps{
		Title:      "Panel",
		Since:      sinceLabel,
		Collectors: dashboardCollectorsView(collectors),
	}).Render(r.Context(), w)
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
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	coll, err := h.svc.GetCollector(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
		default:
			templates.RenderError(w, r, "Error", "Failed to get collector", http.StatusInternalServerError)
		}
		return
	}

	sinceLabel := ensureSinceCookie(w, r)
	var stats *view.CollectorStatsView
	if coll.Stats != nil {
		stats = &view.CollectorStatsView{
			CPUUsagePercent:    coll.Stats.CPUUsagePercent,
			MemTotalMB:         coll.Stats.MemTotalMB,
			MemUsedMB:          coll.Stats.MemUsedMB,
			DiskTotalGB:        coll.Stats.DiskTotalGB,
			DiskUsedGB:         coll.Stats.DiskUsedGB,
			TimestampFormatted: coll.Stats.TimestampFormatted,
			Staleness:          coll.Stats.Staleness,
		}
	}
	view.CollectorOverview(view.CollectorOverviewProps{
		Title:         req.CollectorName,
		CollectorName: req.CollectorName,
		Since:         sinceLabel,
		Stats:         stats,
	}).Render(r.Context(), w)
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
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}
	apps, err := h.svc.ListCollectorApps(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			view.CollectorApps(view.CollectorAppsProps{
				CollectorName: req.CollectorName,
				Apps:          []view.AppView{},
			}).Render(r.Context(), w)
		default:
			templates.RenderError(w, r, "Error", "Failed to get collector apps", http.StatusInternalServerError)
		}
		return
	}

	viewApps := make([]view.AppView, len(apps))
	for i, a := range apps {
		viewApps[i] = view.AppView{Name: a}
	}
	view.CollectorApps(view.CollectorAppsProps{
		CollectorName: req.CollectorName,
		Apps:          viewApps,
	}).Render(r.Context(), w)
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
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	stats, err := h.svc.GetStatsHistory(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this collector", http.StatusForbidden)
		default:
			templates.RenderError(w, r, "Error", "Failed to get stats history", http.StatusInternalServerError)
		}
		return
	}

	// Convert period data to view layer
	cpuPeriods := make([]view.CPUPeriodView, len(stats.CPUPeriods))
	for i, p := range stats.CPUPeriods {
		end := int64(0)
		if p.EndedAt.Valid {
			end = p.EndedAt.Int64
		}
		cpuPeriods[i] = view.CPUPeriodView{
			PeriodStart:  p.StartedAt,
			PeriodEnd:    end,
			UsagePercent: p.UsagePercent,
		}
	}

	memPeriods := make([]view.MemPeriodView, len(stats.MemPeriods))
	for i, p := range stats.MemPeriods {
		end := int64(0)
		if p.EndedAt.Valid {
			end = p.EndedAt.Int64
		}
		memPeriods[i] = view.MemPeriodView{
			PeriodStart: p.StartedAt,
			PeriodEnd:   end,
			UsedMb:      p.UsedMb,
			TotalMb:     p.TotalMb,
		}
	}

	diskPeriods := make([]view.DiskPeriodView, len(stats.DiskPeriods))
	for i, p := range stats.DiskPeriods {
		end := int64(0)
		if p.EndedAt.Valid {
			end = p.EndedAt.Int64
		}
		diskPeriods[i] = view.DiskPeriodView{
			PeriodStart: p.StartedAt,
			PeriodEnd:   end,
			AvailGb:     float64(p.AvailGb),
		}
	}

	view.CollectorStats(view.CollectorStatsProps{
		Title:         req.CollectorName + " Resources",
		CollectorName: req.CollectorName,
		Since:         sinceLabel,
		CPUPeriods:    cpuPeriods,
		MemPeriods:    memPeriods,
		DiskPeriods:   diskPeriods,
	}).Render(r.Context(), w)
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
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	health, err := h.svc.GetAppHealth(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			templates.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			templates.RenderError(w, r, "Error", "Failed to fetch app health", http.StatusInternalServerError)
		}
		return
	}

	view.AppHealth(view.AppHealthProps{
		Health: appHealthViewFromServiceToView(health),
	}).Render(r.Context(), w)
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
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	count, err := h.svc.GetAppErrorCount(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			templates.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			templates.RenderError(w, r, "Error", "Failed to fetch app error count", http.StatusInternalServerError)
		}
		return
	}

	view.AppErrorCount(view.AppErrorCountProps{
		Errors: view.AppErrorCountView{
			Count: count,
			Class: appErrorCountClass(count),
		},
	}).Render(r.Context(), w)
}

// App logs handlers

func (h *handler) getApp(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	level := r.URL.Query().Get("level")
	if level == "" {
		redirectURL := fmt.Sprintf("/panel/collectors/%s/apps/%s?level=%s",
			collectorName, appName, defaultLogLevel)
		http.Redirect(w, r, redirectURL, http.StatusSeeOther)
		return
	}

	sinceLabel, since := readSince(w, r)
	healthReq := panelsvc.GetAppHealthRequest{
		UserID:        userID,
		CollectorName: collectorName,
		AppName:       appName,
		Since:         since,
	}
	if err := healthReq.Validate(); err != nil {
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	uptime, err := h.svc.GetAppHealth(r.Context(), healthReq)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			templates.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			templates.RenderError(w, r, "Error", "Failed to fetch uptime", http.StatusInternalServerError)
		}
		return
	}
	view.App(view.AppProps{
		Title:     appName + " Analytics",
		App:       appName,
		Collector: collectorName,
		Since:     sinceLabel,
		Uptime:    appHealthViewFromServiceToView(uptime),
		Level:     level,
	}).Render(r.Context(), w)
}

func (h *handler) getAppStats(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	_, since := readSince(w, r)

	req := panelsvc.GetAppStatsRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		Since:         since,
	}
	if err := req.Validate(); err != nil {
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	stats, err := h.svc.GetAppStats(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			templates.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			templates.RenderError(w, r, "Error", "Failed to fetch stats", http.StatusInternalServerError)
		}
		return
	}

	view.AppStats(view.AppStatsProps{
		Stats: view.AppStatsData{
			Total:    stats.Total,
			Errors:   stats.Errors,
			Warnings: stats.Warnings,
			Infos:    stats.Infos,
		},
	}).Render(r.Context(), w)
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

func dashboardCollectorsView(collectors []panelsvc.Collector) []view.CollectorView {
	result := make([]view.CollectorView, 0, len(collectors))
	for _, collector := range collectors {
		var stats *view.CollectorStatsView
		if collector.Stats != nil {
			stats = &view.CollectorStatsView{
				CPUUsagePercent:    collector.Stats.CPUUsagePercent,
				MemTotalMB:         collector.Stats.MemTotalMB,
				MemUsedMB:          collector.Stats.MemUsedMB,
				DiskTotalGB:        collector.Stats.DiskTotalGB,
				DiskUsedGB:         collector.Stats.DiskUsedGB,
				TimestampFormatted: collector.Stats.TimestampFormatted,
				Staleness:          collector.Stats.Staleness,
			}
		}
		result = append(result, view.CollectorView{
			Name:        collector.Name,
			RemoteAddr:  collector.RemoteAddr,
			IsConnected: collector.IsConnected,
			ConnectedAt: collector.ConnectedAt,
			LastSeenAt:  collector.LastSeenAt,
			Stats:       stats,
		})
	}
	return result
}

func appHealthViewFromServiceToView(health panelsvc.AppHealth) view.AppHealthView {
	return view.AppHealthView{
		UptimeText:      uptimePercentText(health.UptimePercent, health.PeriodCount),
		UptimeTextClass: uptimePercentClass(health.UptimePercent, health.PeriodCount),
		Bars:            uptimeBarsToView(health.Buckets),
	}
}

func uptimeBarsToView(buckets []panelsvc.HealthBucket) []view.UptimeBarView {
	result := make([]view.UptimeBarView, 0, len(buckets))
	for _, bucket := range buckets {
		status := uptimeBucketStatus(bucket.UpDuration, bucket.DownDuration)
		result = append(result, view.UptimeBarView{
			Class: uptimeBarClass(status),
			Title: uptimeBucketTitle(bucket.Start, bucket.End, bucket.UpDuration, bucket.DownDuration),
		})
	}
	return result
}

func appErrorCountClass(v int64) string {
	if v > 0 {
		return "text-red-400"
	}
	return "text-fg-secondary"
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

	level := r.URL.Query().Get("level")
	if level == "" {
		level = defaultLogLevel
	}

	req := panelsvc.GetLogGroupsRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		Level:         level,
		Since:         since,
		Offset:        parseOffset(r.URL.Query().Get("offset")),
		Limit:         50,
	}
	if err := req.Validate(); err != nil {
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	groups, err := h.svc.GetLogGroups(r.Context(), req)
	if err != nil {
		slog.Error("GetLogGroups failed", "error", err, "collector", req.CollectorName, "app", req.AppName)
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			templates.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			templates.RenderError(w, r, "Error", "Failed to fetch log groups", http.StatusInternalServerError)
		}
		return
	}

	// Update URL without adding history entry
	canonicalURL := fmt.Sprintf("/panel/collectors/%s/apps/%s?level=%s",
		req.CollectorName, req.AppName, level)
	w.Header().Set("HX-Replace-Url", canonicalURL)

	viewGroups := make([]view.LogGroupItem, len(groups))
	for i, g := range groups {
		viewGroups[i] = view.LogGroupItem{
			ID:      g.ID,
			Message: g.Message,
			Level:   g.Level,
			Count:   g.Count,
		}
	}
	viewFilters := make([]view.LevelFilterItem, len(levelFilters))
	for i, f := range levelFilters {
		viewFilters[i] = view.LevelFilterItem{
			Val:   f.Val,
			Label: f.Label,
			Color: f.Color,
		}
	}
	view.LogGroups(view.LogGroupsProps{
		Groups:       viewGroups,
		Level:        level,
		Collector:    req.CollectorName,
		App:          req.AppName,
		LevelFilters: viewFilters,
	}).Render(r.Context(), w)
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
		templates.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
		return
	}

	req := panelsvc.GetLogGroupRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		GroupID:       groupID,
	}
	if err := req.Validate(); err != nil {
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	group, err := h.svc.GetLogGroup(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound), errors.Is(err, panelsvc.ErrLogNotFound):
			templates.RenderError(w, r, "Error", "Group not found", http.StatusNotFound)
		default:
			templates.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
		}
		return
	}

	view.LogGroup(view.LogGroupProps{
		Title:         req.AppName + " Log Group",
		AppName:       req.AppName,
		CollectorName: req.CollectorName,
		Since:         sinceLabel,
		SinceStart:    since.Unix(),
		SinceEnd:      now.Unix(),
		LogGroup: view.LogGroupData{
			ID:        group.ID,
			Message:   group.Message,
			Level:     group.Level,
			Count:     group.Count,
			CreatedAt: group.CreatedAt.Unix(),
			LastLogAt: group.LastLogAt.Unix(),
		},
	}).Render(r.Context(), w)
}

func (h *handler) getLogGroupEntries(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		templates.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
		return
	}

	_, since := readSince(w, r)
	offset := parseOffset(r.URL.Query().Get("offset"))
	limit := 50

	req := panelsvc.ListGroupLogsRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		GroupID:       groupID,
		Since:         since,
		Offset:        offset,
		Limit:         limit + 1,
	}
	if err := req.Validate(); err != nil {
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	entries, err := h.svc.ListGroupLogs(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound), errors.Is(err, panelsvc.ErrLogNotFound):
			templates.RenderError(w, r, "Error", "Group not found", http.StatusNotFound)
		default:
			slog.Error("Failed to list group logs", "error", err, "groupID", req.GroupID, "collector", req.CollectorName, "app", req.AppName)
			templates.RenderError(w, r, "Error", "Failed to fetch log entries", http.StatusInternalServerError)
		}
		return
	}

	nextOffset := offset + limit
	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}

	viewEntries := make([]view.LogEntryItem, len(entries))
	for i, e := range entries {
		viewEntries[i] = view.LogEntryItem{
			ID:        e.ID,
			CreatedAt: e.CreatedAt,
			Context: struct {
				Valid  bool
				String string
			}{
				Valid:  e.Context.Valid,
				String: e.Context.String,
			},
		}
	}
	view.LogGroupEntries(view.LogGroupEntriesProps{
		Entries:       viewEntries,
		CollectorName: req.CollectorName,
		AppName:       req.AppName,
		GroupID:       groupID,
		HasMore:       hasMore,
		NextOffset:    nextOffset,
		Offset:        offset,
		Level:         r.URL.Query().Get("level"),
	}).Render(r.Context(), w)
}

func (h *handler) deleteLogGroup(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		templates.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
		return
	}

	req := panelsvc.DeleteLogGroupRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		GroupID:       groupID,
	}
	if err := req.Validate(); err != nil {
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.svc.DeleteLogGroup(r.Context(), req); err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound), errors.Is(err, panelsvc.ErrLogNotFound):
			templates.RenderError(w, r, "Error", "Group not found", http.StatusNotFound)
		default:
			slog.Error("Failed to delete log group", "error", err, "groupID", req.GroupID, "collector", req.CollectorName, "app", req.AppName)
			templates.RenderError(w, r, "Error", "Failed to delete log group", http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("HX-Redirect", fmt.Sprintf("/panel/collectors/%s/apps/%s", req.CollectorName, req.AppName))
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getLogGroupPlot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Write(logGroupPlotJS)
}

func (h *handler) getLogByID(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}
	ensureSinceCookie(w, r)

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		templates.RenderError(w, r, "Error", "Invalid log ID", http.StatusBadRequest)
		return
	}

	req := panelsvc.GetLogByIDRequest{
		UserID:        userID,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		ID:            id,
	}
	if err := req.Validate(); err != nil {
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	logEntry, err := h.svc.GetLogByID(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			templates.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrLogNotFound):
			templates.RenderError(w, r, "Error", "Log not found", http.StatusNotFound)
		default:
			templates.RenderError(w, r, "Error", "Failed to get log", http.StatusInternalServerError)
		}
		return
	}

	view.Log(view.LogProps{
		Title:         fmt.Sprintf("Log #%d", logEntry.ID),
		CollectorName: req.CollectorName,
		AppName:       req.AppName,
		Log: view.LogEntryDetail{
			ID:      logEntry.ID,
			Level:   logEntry.Level,
			Message: logEntry.Message,
			Context: struct {
				Valid  bool
				String string
			}{
				Valid:  logEntry.Context.Valid,
				String: logEntry.Context.String,
			},
			CreatedAt:      time.Unix(logEntry.CreatedAt, 0),
			TraceIDDisplay: logTraceIDDisplay(logEntry.TraceID),
			GroupID:        logEntry.GroupID.Int64,
		},
	}).Render(r.Context(), w)
}

func logTraceIDDisplay(traceID sql.NullString) string {
	if !traceID.Valid {
		return "Not present"
	}
	return traceID.String
}

func parseOffset(offsetParam string) int {
	if offsetParam == "" {
		return 0
	}
	offset, err := strconv.Atoi(offsetParam)
	if err != nil {
		return 0
	}
	return offset
}

// postUpdate handles the update-all endpoint.
// It triggers a full update: notifies connected collectors and starts a
// panel self-update in the background.
func (h *handler) postUpdate(w http.ResponseWriter, r *http.Request) {
	userID, err := auth.RequireOrRedirect(w, r)
	if err != nil {
		return
	}
	// Only admin users can trigger updates.
	// TODO: replace with proper role-based access control.
	if userID != 1 {
		http.Error(w, "Forbidden: admin access required", http.StatusForbidden)
		return
	}

	result, err := h.updateSvc.UpdateAll()
	if err != nil {
		slog.Error("Update failed", "error", err)
		http.Error(w, "Update failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(result); err != nil {
		slog.Error("Failed to write update response", "error", err)
	}
}
