package panel

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
	panelsvc "tracer/apps/panel/internal/services/panel"
	"tracer/apps/panel/web/handlers/panel/view"
	"tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/templates"
	"tracer/libs/types"
)

//go:embed views/log-group-plot.js
var logGroupPlotJS []byte

type handler struct {
	svc         *panelsvc.Service
	authService *authsvc.Service
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

func InitMux(svc *panelsvc.Service, authService *authsvc.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		svc:         svc,
		authService: authService,
	}

	// Dashboard
	m.HandleFunc("GET /", h.getDashboard)
	m.HandleFunc("GET /collectors", h.getDashboard)

	// Collector
	m.HandleFunc("GET /collectors/{collector}", h.getCollector)
	m.HandleFunc("GET /collectors/{collector}/apps", h.getCollectorApps)
	m.HandleFunc("GET /collectors/{collector}/resources", h.getCollectorResources)
	m.HandleFunc("GET /collectors/{collector}/stats", h.getCollectorStats)

	// Collector add app + config
	m.HandleFunc("GET /collectors/{collector}/apps/new", h.getNewAppPage)
	m.HandleFunc("POST /collectors/{collector}/apps", h.createApp)
	m.HandleFunc("POST /collectors/{collector}/apps/preview", h.previewAppFormat)
	m.HandleFunc("POST /collectors/{collector}/apps/health-probe", h.probeAppHealth)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/settings", h.getEditAppPage)
	m.HandleFunc("PUT /collectors/{collector}/apps/{app}", h.updateApp)
	m.HandleFunc("DELETE /collectors/{collector}/apps/{app}", h.deleteApp)

	// Collector metrics config
	m.HandleFunc("GET /collectors/{collector}/stats/configure", h.getCollectorStatsConfig)
	m.HandleFunc("POST /collectors/{collector}/stats/configure", h.saveCollectorStatsConfig)

	// Install script (returns shell script for collector setup)
	m.HandleFunc("GET /collectors/{collector}/install-script", h.getInstallScript)

	// Last update indicators
	m.HandleFunc("GET /collectors/{collector}/last-update", h.getCollectorLastUpdate)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/last-update", h.getAppLastUpdate)

	// App logs
	m.HandleFunc("GET /collectors/{collector}/apps/{app}", h.getApp)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/stats", h.getAppStats)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/uptime", h.getAppUptime)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups", h.getLogGroups)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups/{groupID}", h.getLogGroupByID)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups/{groupID}/entries", h.getLogGroupEntries)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/groups/{groupID}/summary", h.getLogGroupSummary)
	m.HandleFunc("DELETE /collectors/{collector}/apps/{app}/logs/groups/{groupID}", h.deleteLogGroup)
	m.HandleFunc("GET /collectors/{collector}/apps/{app}/logs/{id}", h.getLogByID)

	// Static assets
	m.HandleFunc("GET /logs/groups/plot", h.getLogGroupPlot)

	return m
}

// Dashboard handlers

// canModifyCollector checks if the user has permission to modify the collector.
func (h *handler) canModifyCollector(ctx context.Context, user domain.User, collectorName string) bool {
	if user.Role == domain.RoleAdmin {
		return true
	}
	err := h.svc.CanModifyCollector(ctx, user.ID, collectorName)
	return err == nil
}

func (h *handler) getDashboard(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	sinceLabel := ensureSinceCookie(w, r)
	collectors, err := h.svc.ListCollectors(r.Context(), user.ID)
	if err != nil {
		templates.RenderError(w, r, "Error", "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	collectorViews := make([]view.CollectorView, 0, len(collectors))
	for _, collector := range collectors {
		apps, err := h.svc.ListCollectorApps(r.Context(), panelsvc.ListCollectorAppsRequest{
			UserID:        user.ID,
			CollectorName: collector.Name,
		})
		if err != nil {
			slog.Error("could not count accessible apps", "collector", collector.Name, "error", err)
		}
		collectorViews = append(collectorViews, view.CollectorView{
			Name:               collector.Name,
			RemoteAddr:         collector.RemoteAddr,
			IsConnected:        collector.IsConnected,
			ConnectedAt:        collector.ConnectedAt,
			LastSeenAt:         collector.LastSeenAt,
			Stats:              collectorStatsView(collector.Stats),
			AccessibleAppCount: len(apps),
		})
	}

	view.Dashboard(view.DashboardProps{
		Title:      "Panel",
		Since:      sinceLabel,
		Collectors: collectorViews,
	}).Render(r.Context(), w)
}

// Collector handlers

func (h *handler) getCollector(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	req := panelsvc.GetCollectorRequest{
		UserID:        user.ID,
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

	apps, err := h.svc.ListCollectorApps(r.Context(), panelsvc.ListCollectorAppsRequest{
		UserID:        user.ID,
		CollectorName: req.CollectorName,
	})
	if err != nil {
		// Existence and access were validated by GetCollector; a count
		// failure only degrades the skeleton to its empty state.
		slog.Error("could not count accessible apps", "collector", req.CollectorName, "error", err)
		apps = nil
	}

	sinceLabel := ensureSinceCookie(w, r)
	view.CollectorOverview(view.CollectorOverviewProps{
		Title:              req.CollectorName,
		CollectorName:      req.CollectorName,
		Since:              sinceLabel,
		Stats:              collectorStatsView(coll.Stats),
		AccessibleAppCount: len(apps),
	}).Render(r.Context(), w)
}

// getCollectorStatsConfig renders the metrics collection configuration page.
func (h *handler) getCollectorStatsConfig(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}
	collectorName := r.PathValue("collector")
	if !h.canModifyCollector(r.Context(), user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	cfg, err := h.loadCollectorConfig(r.Context(), collectorName)
	if err != nil {
		http.Error(w, "Failed to load config", http.StatusInternalServerError)
		return
	}
	stats := cfg.GetStats()

	view.StatsConfigPage(view.StatsConfigProps{
		Title:                 "Metrics config - " + collectorName,
		Collector:             collectorName,
		Interval:              stats.Interval,
		CPU:                   stats.CPU,
		Mem:                   stats.Mem,
		Disk:                  stats.Disk,
		CPUDeltaPercent:       stats.CPUDeltaPercent,
		MemDeltaKB:            stats.MemDeltaKB,
		DiskDeltaKB:           stats.DiskDeltaKB,
		CPUThresholdPercent:   stats.CPUThresholdPercent,
		MemThresholdPercent:   stats.MemThresholdPercent,
		DiskThresholdPercent:  stats.DiskThresholdPercent,
		ResourceNotifyMinutes: stats.ResourceNotifyMinutes,
	}).Render(r.Context(), w)
}

func (h *handler) saveCollectorStatsConfig(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}
	collectorName := r.PathValue("collector")
	if !h.canModifyCollector(r.Context(), user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	interval := strings.TrimSpace(r.FormValue("interval"))
	if interval == "" {
		interval = "30s"
	}

	// Read optional per-collector overrides; nil means "use global default".
	cpuDelta := parseFloatPtr(r.FormValue("cpu_delta_percent"))
	memDelta := parseIntPtr(r.FormValue("mem_delta_kb"))
	diskDelta := parseIntPtr(r.FormValue("disk_delta_kb"))
	cpuThreshold := parseFloatPtr(r.FormValue("cpu_threshold_percent"))
	memThreshold := parseFloatPtr(r.FormValue("mem_threshold_percent"))
	diskThreshold := parseFloatPtr(r.FormValue("disk_threshold_percent"))
	notifyMin := parseIntPtr(r.FormValue("resource_notify_minutes"))

	stats := types.StatsConfig{
		Interval:              interval,
		CPU:                   r.FormValue("cpu") == "on",
		Mem:                   r.FormValue("mem") == "on",
		Disk:                  r.FormValue("disk") == "on",
		CPUDeltaPercent:       cpuDelta,
		MemDeltaKB:            memDelta,
		DiskDeltaKB:           diskDelta,
		CPUThresholdPercent:   cpuThreshold,
		MemThresholdPercent:   memThreshold,
		DiskThresholdPercent:  diskThreshold,
		ResourceNotifyMinutes: notifyMin,
	}
	if _, err := time.ParseDuration(interval); err != nil {
		view.StatsConfigPage(view.StatsConfigProps{
			Title:                 "Metrics config - " + collectorName,
			Collector:             collectorName,
			Interval:              interval,
			CPU:                   stats.CPU,
			Mem:                   stats.Mem,
			Disk:                  stats.Disk,
			CPUDeltaPercent:       cpuDelta,
			MemDeltaKB:            memDelta,
			DiskDeltaKB:           diskDelta,
			CPUThresholdPercent:   cpuThreshold,
			MemThresholdPercent:   memThreshold,
			DiskThresholdPercent:  diskThreshold,
			ResourceNotifyMinutes: notifyMin,
			Status:                "Invalid interval: " + err.Error(),
			StatusOK:              false,
		}).Render(r.Context(), w)
		return
	}

	cfg, err := h.loadCollectorConfig(r.Context(), collectorName)
	if err != nil {
		http.Error(w, "Failed to load config", http.StatusInternalServerError)
		return
	}
	cfg.Stats = &stats
	if err := h.saveCollectorConfig(r.Context(), collectorName, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	view.StatsConfigSaved(view.StatsConfigSavedProps{
		Collector: collectorName,
	}).Render(r.Context(), w)
}

// getCollectorApps returns the apps grid fragment with uptime and
// error-count stats.
func (h *handler) getCollectorApps(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}
	_, since := readSince(w, r)

	req := panelsvc.ListCollectorAppsWithStatsRequest{
		UserID:        user.ID,
		CollectorName: r.PathValue("collector"),
		Since:         since,
	}
	if err := req.Validate(); err != nil {
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}
	apps, err := h.svc.ListCollectorAppsWithStats(r.Context(), req)
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
		lastUpdate := view.LastUpdateView{}
		if !a.LastUpdateAt.IsZero() {
			age := time.Since(a.LastUpdateAt)
			lastUpdate = view.LastUpdateView{Text: view.FormatAgo(age), Class: view.LastUpdateClass(age, a.Interval)}
		}
		viewApps[i] = view.AppView{
			Name:            a.Name,
			HasUptime:       a.HasUptime,
			Bars:            uptimeBarsToView(a.Bars),
			ErrorCount:      errorCountText(a.HasLogs, a.ErrorCount),
			ErrorTrend:      errorTrendText(a.ErrorTrendValid, a.ErrorTrendPct),
			ErrorTrendClass: errorTrendClass(a.ErrorTrendValid, a.ErrorTrendPct),
			ErrorTrendUp:    a.ErrorTrendValid && a.ErrorTrendPct > 0,
			LastUpdate:      lastUpdate,
		}
	}
	view.CollectorApps(view.CollectorAppsProps{
		CollectorName: req.CollectorName,
		Apps:          viewApps,
	}).Render(r.Context(), w)
}

func (h *handler) getCollectorResources(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	sinceLabel, since := readSince(w, r)

	req := panelsvc.GetStatsHistoryRequest{
		UserID:        user.ID,
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

// getCollectorStats returns the collector stats row as an HTMX fragment.
func (h *handler) getCollectorStats(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	req := panelsvc.GetCollectorRequest{
		UserID:        user.ID,
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

	view.StatsRow(collectorStatsView(coll.Stats), req.CollectorName).Render(r.Context(), w)
}

func (h *handler) getCollectorLastUpdate(w http.ResponseWriter, r *http.Request) {
	_, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	if err := domain.ValidateCollectorName(collectorName); err != nil {
		http.Error(w, "invalid collector name", http.StatusBadRequest)
		return
	}

	activity, ok, err := h.svc.CollectorActivity(r.Context(), collectorName)
	if err != nil {
		slog.Error("Could not read collector activity", "collector", collectorName, "error", err)
		return
	}
	if !ok {
		view.LastUpdateBadge(view.LastUpdateView{}).Render(r.Context(), w)
		return
	}

	age := time.Since(activity.At)
	view.LastUpdateBadge(view.LastUpdateView{
		Text:  view.FormatAgo(age),
		Class: view.LastUpdateClass(age, activity.Interval),
	}).Render(r.Context(), w)
}

// App logs handlers

func (h *handler) getApp(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
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
		UserID:        user.ID,
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
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	_, since := readSince(w, r)

	req := panelsvc.GetAppStatsRequest{
		UserID:        user.ID,
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
			HasLogs:  stats.HasLogs,
			Total:    stats.Total,
			Errors:   stats.Errors,
			Warnings: stats.Warnings,
			Infos:    stats.Infos,
		},
	}).Render(r.Context(), w)
}

// getAppUptime returns the app uptime panel as an HTMX fragment.
func (h *handler) getAppUptime(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	_, since := readSince(w, r)

	req := panelsvc.GetAppHealthRequest{
		UserID:        user.ID,
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
			templates.RenderError(w, r, "Error", "Failed to fetch uptime", http.StatusInternalServerError)
		}
		return
	}

	view.UptimePanel(appHealthViewFromServiceToView(health)).Render(r.Context(), w)
}

// getAppLastUpdate returns the app's "updated X ago" indicator as an HTMX
// fragment.
func (h *handler) getAppLastUpdate(w http.ResponseWriter, r *http.Request) {
	_, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	activity, ok, err := h.svc.AppActivity(r.Context(), collectorName, appName)
	if err != nil {
		slog.Error("Could not read app activity", "collector", collectorName, "app", appName, "error", err)
		return
	}
	if !ok {
		view.LastUpdateBadge(view.LastUpdateView{}).Render(r.Context(), w)
		return
	}

	age := time.Since(activity.At)
	view.LastUpdateBadge(view.LastUpdateView{
		Text:  view.FormatAgo(age),
		Class: view.LastUpdateClass(age, activity.Interval),
	}).Render(r.Context(), w)
}

func readSince(w http.ResponseWriter, r *http.Request) (string, time.Time) {
	return templates.ReadSince(w, r)
}

func ensureSinceCookie(w http.ResponseWriter, r *http.Request) string {
	return templates.EnsureSinceCookie(w, r)
}

// collectorStatsView converts service-level collector stats to the view layer,
// or nil when the collector has no stats yet.
func collectorStatsView(stats *panelsvc.CollectorStats) *view.CollectorStatsView {
	if stats == nil {
		return nil
	}
	return &view.CollectorStatsView{
		CPUUsagePercent:    stats.CPUUsagePercent,
		MemTotalMB:         stats.MemTotalMB,
		MemUsedMB:          stats.MemUsedMB,
		DiskTotalGB:        stats.DiskTotalGB,
		DiskUsedGB:         stats.DiskUsedGB,
		TimestampFormatted: stats.TimestampFormatted,
		Staleness:          stats.Staleness,
	}
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
			Title: uptimeBucketTitle(bucket.Start, bucket.End, bucket.UpDuration, bucket.DownDuration, bucket.Checks),
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

// errorCountText renders the app-grid error count, or "N/A" when the app
// never received logs.
func errorCountText(hasLogs bool, errorCount int64) string {
	if !hasLogs {
		return "N/A"
	}
	return strconv.FormatInt(errorCount, 10)
}

// errorTrendText renders the error trend percentage.
func errorTrendText(valid bool, pct float64) string {
	if !valid {
		return ""
	}
	return fmt.Sprintf("%.0f%%", math.Abs(pct))
}

// errorTrendClass colors the error trend: green for down, red for up, neutral
// for flat or unavailable.
func errorTrendClass(valid bool, pct float64) string {
	if !valid {
		return "text-fg-secondary"
	}
	if pct > 0 {
		return "text-red-400"
	}
	if pct < 0 {
		return "text-green-400"
	}
	return "text-fg-secondary"
}

// errorCountClass colors the app-grid error count red when errors exist and
// the app has logs.
func errorCountClass(hasLogs bool, v int64) string {
	if !hasLogs {
		return "text-fg-secondary"
	}
	if v > 0 {
		return "text-red-400"
	}
	return "text-fg-secondary"
}

func uptimeBarClass(status string) string {
	switch status {
	case "up":
		return "bg-green-500 border border-transparent uptime-bar"
	case "down":
		return "bg-red-500 border border-transparent uptime-bar"
	case "mixed":
		return "bg-yellow-500 border border-transparent uptime-bar"
	default:
		return "bg-fg/10 border border-fg/5"
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

func uptimeBucketTitle(start, end time.Time, upDuration, downDuration time.Duration, checks int) string {
	title := fmt.Sprintf("%s - %s", start.Format("2006-01-02 15:04"), end.Format("15:04"))
	total := upDuration + downDuration
	if checks > 0 {
		title += fmt.Sprintf(" • %s", checkCountText(checks))
	}
	if total == 0 {
		return title + " • no uptime data"
	}
	return fmt.Sprintf("%s • %.2f%% uptime", title, (float64(upDuration)/float64(total))*100)
}

func checkCountText(checks int) string {
	if checks == 1 {
		return "1 check"
	}
	return fmt.Sprintf("%d checks", checks)
}

func (h *handler) getLogGroups(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	_, since := readSince(w, r)

	level := r.URL.Query().Get("level")
	if level == "" {
		level = defaultLogLevel
	}

	req := panelsvc.GetLogGroupsRequest{
		UserID:        user.ID,
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
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			templates.RenderError(w, r, "Error", "Collector not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			templates.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			templates.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
		default:
			slog.Error("GetLogGroups failed", "error", err, "collector", req.CollectorName, "app", req.AppName)
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
	user, err := auth.RequireUser(w, r)
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
		UserID:        user.ID,
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
			Since:     now.Unix(),
			NewLogs:   0,
		},
	}).Render(r.Context(), w)
}

// getLogGroupSummary returns the log group metadata (count, first, latest)
// as an HTMX fragment.
func (h *handler) getLogGroupSummary(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		templates.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
		return
	}

	since := int64(0)
	if raw := r.URL.Query().Get("since"); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			since = v
		}
	}

	req := panelsvc.GetLogGroupSummaryRequest{
		UserID:        user.ID,
		CollectorName: r.PathValue("collector"),
		AppName:       r.PathValue("app"),
		GroupID:       groupID,
		Since:         since,
	}
	if err := req.Validate(); err != nil {
		templates.RenderError(w, r, "Error", err.Error(), http.StatusBadRequest)
		return
	}

	summary, err := h.svc.GetLogGroupSummary(r.Context(), req)
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

	view.LogGroupSummary(view.LogGroupData{
		ID:        summary.ID,
		Message:   summary.Message,
		Level:     summary.Level,
		Count:     summary.Count,
		CreatedAt: summary.CreatedAt.Unix(),
		LastLogAt: summary.LastLogAt.Unix(),
		Since:     time.Now().Unix(),
		NewLogs:   summary.NewLogs,
	}).Render(r.Context(), w)
}

func (h *handler) getLogGroupEntries(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
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
		UserID:        user.ID,
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
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		templates.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
		return
	}

	req := panelsvc.DeleteLogGroupRequest{
		UserID:        user.ID,
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
	user, err := auth.RequireUser(w, r)
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
		UserID:        user.ID,
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

// ── App form ────────────────────────────────────────────────────────────────

func (h *handler) getNewAppPage(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}
	collectorName := r.PathValue("collector")
	if !h.canModifyCollector(r.Context(), user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	view.AppFormPage(view.AppFormProps{
		Title:     "Add app - " + collectorName,
		Collector: collectorName,
	}).Render(r.Context(), w)
}

func (h *handler) getEditAppPage(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}
	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	if !h.canModifyCollector(r.Context(), user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Confirm the app exists (DB-backed), mirroring the app detail page.
	_, since := readSince(w, r)
	healthReq := panelsvc.GetAppHealthRequest{
		UserID:        user.ID,
		CollectorName: collectorName,
		AppName:       appName,
		Since:         since,
	}
	if _, err := h.svc.GetAppHealth(r.Context(), healthReq); err != nil {
		switch {
		case errors.Is(err, panelsvc.ErrCollectorNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		case errors.Is(err, panelsvc.ErrAccessDenied):
			http.Error(w, "access denied", http.StatusForbidden)
		case errors.Is(err, panelsvc.ErrAppNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}

	props := view.AppFormProps{
		Title:       "Edit app - " + appName,
		Collector:   collectorName,
		ServiceName: appName,
		IsEdit:      true,
	}
	if cfg, err := h.loadCollectorConfig(r.Context(), collectorName); err == nil {
		if app, ok := cfg.GetApp(appName); ok {
			guess := view.FormatGuessView{Collector: collectorName}
			if app.ParserID != "" {
				parser := cfg.Parsers[app.ParserID]
				guess.ParserType = parser.Type
				guess.ParserParams = parser.Params
			}
			if app.Source != nil {
				guess.SourceType = app.Source.Type
				guess.SourceParams = paramsToString(app.Source.Params)
				switch app.Source.Type {
				case "journalctl":
					guess.Service = app.Source.Params["unit"]
				case "kubectl":
					guess.Service = app.Source.Params["resource"]
				}
			}
			props.Guess = guess
			if app.Health != nil {
				props.URL = app.Health.Params["url"]
				props.Interval = app.Health.Params["interval"]
				props.ExpectedStatus = app.Health.Params["expected_status"]
			}
		}
	}
	view.AppFormPage(props).Render(r.Context(), w)
}

func (h *handler) previewAppFormat(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}
	collectorName := r.PathValue("collector")
	if !h.canModifyCollector(r.Context(), user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	service := strings.TrimSpace(r.FormValue("service"))
	if service == "" {
		service = name
	}

	render := func(guess view.FormatGuessView) {
		view.AppForm(view.AppFormProps{
			Collector:      collectorName,
			ServiceName:    name,
			URL:            strings.TrimSpace(r.FormValue("url")),
			Interval:       r.FormValue("interval"),
			ExpectedStatus: r.FormValue("expected_status"),
			Guess:          guess,
		}).Render(r.Context(), w)
	}

	guess := view.FormatGuessView{
		Collector: collectorName,
		Service:   service,
	}

	if service == "" {
		render(guess)
		return
	}

	favour := parseFavour(r.FormValue("favour_field"), r.FormValue("favour_value"))
	result, err := h.svc.PreviewFormat(r.Context(), collectorName, service, favour)
	if err != nil {
		guess.Status = "Could not detect format: " + err.Error()
		guess.StatusOK = false
		render(guess)
		return
	}

	switch {
	case result.NotFound:
		guess.NotFound = true
		guess.Status = service + " was not found on the collector."
		guess.StatusOK = false
	case result.Conflict != nil:
		guess.Conflict = &view.FormatConflictView{
			Field:   result.Conflict.Field,
			Options: result.Conflict.Options,
			Samples: formatConflictSamples(result.Conflict),
		}
	case result.Error != "":
		guess.Status = result.Error
		guess.StatusOK = false
	default:
		guess.Detected = true
		guess.Status = detectedFormatText(result.App, result.Parser)
		guess.StatusOK = true
		if result.App.Source != nil {
			guess.SourceType = result.App.Source.Type
			guess.SourceParams = paramsToString(result.App.Source.Params)
		}
		guess.ParserType = result.Parser.Type
		guess.ParserParams = result.Parser.Params
		if len(result.Samples) > 5 {
			result.Samples = result.Samples[:5]
		}
		guess.Samples = result.Samples
	}
	render(guess)
}

// formatConflictSamples pairs each clashing log record with the conflicting
// field value it carries, so the conflict UI can show the user the data
// behind the decision.
func formatConflictSamples(c *types.FormatConflict) []view.FormatSampleView {
	samples := make([]view.FormatSampleView, 0, 2)
	if c.FirstLine != "" {
		samples = append(samples, view.FormatSampleView{Line: c.FirstLine, Value: c.PrevValue})
	}
	if c.CurrLine != "" {
		samples = append(samples, view.FormatSampleView{Line: c.CurrLine, Value: c.CurrValue})
	}
	return samples
}

// detectedFormatText summarizes a successful guess for the log source label
// status, e.g. "Detected journalctl / json format".
func detectedFormatText(app types.AppSpec, parser types.ParserSpec) string {
	if app.Source != nil && app.Source.Type != "" {
		return fmt.Sprintf("Detected %s / %s format", app.Source.Type, parser.Type)
	}
	if parser.Type != "" {
		return fmt.Sprintf("Detected %s format", parser.Type)
	}
	return "Detected format"
}

// probeAppHealth runs the HTTP check the app form's "URL to check" field
// targets, updating the label status while the user fills the form. The probe
// reflects the collector's own check semantics so a green result here means a
// green uptime bar after save.
func (h *handler) probeAppHealth(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}
	collectorName := r.PathValue("collector")
	if !h.canModifyCollector(r.Context(), user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	rawURL := strings.TrimSpace(r.FormValue("url"))
	rawExpected := strings.TrimSpace(r.FormValue("expected_status"))
	text, ok := healthProbeText(r.Context(), rawURL, rawExpected)
	templates.FieldStatus("app-health-status", text, ok, false).Render(r.Context(), w)
}

// healthProbeText reports whether the given URL answers with an expected HTTP
// status. An empty URL and unparsable addresses yield an empty result so the
// label status stays hidden until the user types something checkable.
func healthProbeText(ctx context.Context, rawURL, rawExpected string) (string, bool) {
	expected := parseExpectedStatuses(rawExpected)
	if rawURL == "" {
		return "", false
	}
	probeCtx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return shortHealthError(err), false
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Error("could not close health probe body", "error", cerr)
		}
	}()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16)); err != nil {
		return shortHealthError(err), false
	}
	if isExpectedHTTPStatus(resp.StatusCode, expected) {
		return fmt.Sprintf("up (%d)", resp.StatusCode), true
	}
	if len(expected) == 0 {
		return fmt.Sprintf("down (%d)", resp.StatusCode), false
	}
	return fmt.Sprintf("status differs (got %d, want %s)", resp.StatusCode, rawExpected), false
}

// healthProbeTimeout caps how long a form-time URL probe may take.
const healthProbeTimeout = 5 * time.Second

func shortHealthError(err error) string {
	text := err.Error()
	if len(text) > 80 {
		text = text[:77] + "..."
	}
	return "unreachable: " + text
}

// isExpectedHTTPStatus mirrors the collector's health check: an empty expected
// set accepts any 2xx, otherwise only the listed codes.
func isExpectedHTTPStatus(status int, expected map[int]struct{}) bool {
	if len(expected) == 0 {
		return status >= 200 && status < 300
	}
	_, ok := expected[status]
	return ok
}

// parseExpectedStatuses splits a comma/space-separated status code list.
func parseExpectedStatuses(raw string) map[int]struct{} {
	expected := map[int]struct{}{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		code, err := strconv.Atoi(part)
		if err != nil || code <= 0 {
			continue
		}
		expected[code] = struct{}{}
	}
	return expected
}

// parseFavour builds a parser spec favour from a conflict field/value pair.
// The special field name "type" favours the parser type; any other field
// favours a specific parser param mapping.
func parseFavour(field, value string) types.ParserSpec {
	if field == "" || value == "" {
		return types.ParserSpec{}
	}
	if field == "type" {
		return types.ParserSpec{Type: value}
	}
	return types.ParserSpec{Params: map[string]string{field: value}}
}

func (h *handler) updateApp(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}
	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	if !h.canModifyCollector(r.Context(), user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	cfg, err := h.loadCollectorConfig(r.Context(), collectorName)
	if err != nil {
		http.Error(w, "Failed to load config", http.StatusInternalServerError)
		return
	}

	sourceType, src, parser, hasSource, srcErr := sourceFromForm(r)
	if srcErr != nil {
		http.Error(w, "invalid source parameters", http.StatusBadRequest)
		return
	}
	health := healthFromForm(r)
	sourceCleared := sourceType == "" && strings.TrimSpace(r.FormValue("parser_type")) == ""
	_, exists := cfg.GetApp(appName)

	if hasSource {
		if exists {
			err = cfg.SetAppSource(appName, src, parser)
		} else {
			err = cfg.AddApp(appName, src, parser)
		}
	} else if sourceCleared && exists {
		err = cfg.ClearAppSource(appName)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, exists = cfg.GetApp(appName)

	if health != nil {
		if exists {
			err = cfg.SetAppHealth(appName, health.Params["url"], health.Params["interval"], health.Params["expected_status"])
		} else {
			err = cfg.AddUptimeApp(appName, health.Params["url"], health.Params["interval"], health.Params["expected_status"])
		}
	} else if exists {
		err = cfg.ClearAppHealth(appName)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.saveCollectorConfig(r.Context(), collectorName, cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.renderAppSaved(w, r, collectorName, appName, true)
}

func (h *handler) createApp(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}
	collectorName := r.PathValue("collector")
	if !h.canModifyCollector(r.Context(), user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	appName := strings.TrimSpace(r.FormValue("name"))
	renderError := func(msg string) {
		view.AppForm(view.AppFormProps{
			Collector:      collectorName,
			ServiceName:    appName,
			URL:            strings.TrimSpace(r.FormValue("url")),
			Interval:       r.FormValue("interval"),
			ExpectedStatus: r.FormValue("expected_status"),
			Error:          msg,
		}).Render(r.Context(), w)
	}
	if appName == "" {
		renderError("app name is required")
		return
	}
	if err := domain.ValidateAppName(appName); err != nil {
		renderError(err.Error())
		return
	}

	sourceType, src, parser, hasSource, srcErr := sourceFromForm(r)
	if srcErr != nil {
		http.Error(w, "invalid source parameters", http.StatusBadRequest)
		return
	}
	health := healthFromForm(r)
	if sourceType != "" && !hasSource {
		renderError("parser type is required when a log source is set")
		return
	}

	cfg, err := h.loadCollectorConfig(r.Context(), collectorName)
	if err != nil {
		templates.RenderError(w, r, "Error", "Failed to load config", http.StatusInternalServerError)
		return
	}

	if !hasSource && sourceType == "" && health == nil {
		if err := h.svc.GuessFormatAndSave(r.Context(), collectorName, appName); err != nil {
			switch {
			case errors.Is(err, panelsvc.ErrAppNoSource):
				http.Error(w, "an app needs at least a log source or an uptime check; none could be detected", http.StatusUnprocessableEntity)
			default:
				http.Error(w, err.Error(), http.StatusBadGateway)
			}
			return
		}
		h.renderAppSaved(w, r, collectorName, appName, false)
		return
	}

	if !hasSource && sourceType == "" {
		service := strings.TrimSpace(r.FormValue("service"))
		if service == "" {
			service = appName
		}
		result, gErr := h.svc.PreviewFormat(r.Context(), collectorName, service, types.ParserSpec{})
		if gErr == nil && !result.NotFound && result.Error == "" && result.App.Source != nil {
			hasSource = true
			src = *result.App.Source
			parser = types.ParserSpec{Type: result.Parser.Type, Params: result.Parser.Params}
		}
	}

	if hasSource {
		if err := cfg.AddApp(appName, src, parser); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if health != nil {
		if _, ok := cfg.GetApp(appName); ok {
			err = cfg.SetAppHealth(appName, health.Params["url"], health.Params["interval"], health.Params["expected_status"])
		} else {
			err = cfg.AddUptimeApp(appName, health.Params["url"], health.Params["interval"], health.Params["expected_status"])
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if _, ok := cfg.GetApp(appName); !ok {
		http.Error(w, "an app needs at least a log source or an uptime check; none could be detected", http.StatusUnprocessableEntity)
		return
	}

	if err := h.saveCollectorConfig(r.Context(), collectorName, cfg); err != nil {
		templates.RenderError(w, r, "Error", "Failed to save config", http.StatusInternalServerError)
		return
	}

	h.renderAppSaved(w, r, collectorName, appName, false)
}

// renderAppSaved renders the post-save fragment for both create and edit flows.
// HTMX clients will follow the fragment's hx-get to load the updated content.
func (h *handler) renderAppSaved(w http.ResponseWriter, r *http.Request, collector, appName string, isEdit bool) {
	view.AppSaved(view.AppSavedProps{
		Collector: collector,
		AppName:   appName,
		IsEdit:    isEdit,
	}).Render(r.Context(), w)
}

// loadCollectorConfig loads and decodes the stored collector config.
func (h *handler) loadCollectorConfig(ctx context.Context, collectorName string) (types.CollectorConfig, error) {
	existing, err := h.svc.FetchCollectorConfig(ctx, collectorName)
	if err != nil {
		return types.CollectorConfig{}, fmt.Errorf("could not load config: %w", err)
	}
	var cfg types.CollectorConfig
	if existing != "" {
		if jErr := json.Unmarshal([]byte(existing), &cfg); jErr != nil {
			return types.CollectorConfig{}, fmt.Errorf("invalid stored config: %w", jErr)
		}
	}
	return cfg, nil
}

// saveCollectorConfig marshals and persists the collector config.
func (h *handler) saveCollectorConfig(ctx context.Context, collectorName string, cfg types.CollectorConfig) error {
	updated, err := json.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}
	if err := h.svc.SaveCollectorConfig(ctx, collectorName, string(updated)); err != nil {
		return fmt.Errorf("could not save config: %w", err)
	}
	return nil
}

// paramsToString renders a params map as "key: value" lines for form prefill.
func paramsToString(params map[string]string) string {
	if len(params) == 0 {
		return ""
	}
	out := make([]string, 0, len(params))
	for k, v := range params {
		out = append(out, k+": "+v)
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

func (h *handler) deleteApp(w http.ResponseWriter, r *http.Request) {
	user, err := auth.RequireUser(w, r)
	if err != nil {
		return
	}

	collectorName := r.PathValue("collector")
	if !h.canModifyCollector(r.Context(), user, collectorName) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	appName := r.PathValue("app")

	if err := h.svc.DeleteCollectorApp(r.Context(), collectorName, appName); err != nil {
		slog.Error("Failed to delete app", "error", err, "collector", collectorName, "app", appName)
		http.Error(w, "Failed to delete app", http.StatusInternalServerError)
		return
	}

	// Redirect back to the collector page after deletion.
	w.Header().Set("HX-Redirect", fmt.Sprintf("/panel/collectors/%s", collectorName))
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) getInstallScript(w http.ResponseWriter, r *http.Request) {
	collectorName := r.PathValue("collector")
	if collectorName == "" {
		http.Error(w, "collector name is required", http.StatusBadRequest)
		return
	}

	apiKey := r.Header.Get("X-API-Key")
	if apiKey == "" {
		http.Error(w, "X-API-Key header is required", http.StatusUnauthorized)
		return
	}

	if _, err := h.authService.ValidateCollectorAPIKey(r.Context(), apiKey); err != nil {
		http.Error(w, "invalid API key", http.StatusForbidden)
		return
	}

	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	panelHost := scheme + "://" + host

	w.Header().Set("Content-Type", "text/x-shellscript")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"install-%s.sh\"", collectorName))
	fmt.Fprintf(w, "#!/bin/bash\nset -euo pipefail\n\nsudo tracer-collector install\nsudo tracer-collector set name %s\nsudo tracer-collector set api-key %s\nsudo tracer-collector set panel-host %s\nsudo systemctl restart tracer-collector\n", collectorName, apiKey, panelHost)
}

func logTraceIDDisplay(traceID sql.NullString) string {
	if !traceID.Valid || traceID.String == "" {
		return "-"
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

// healthFromForm returns an HTTP health check when the form includes a URL.
func healthFromForm(r *http.Request) *types.HealthCheckSpec {
	url := strings.TrimSpace(r.FormValue("url"))
	if url == "" {
		return nil
	}
	interval := strings.TrimSpace(r.FormValue("interval"))
	if interval == "" {
		interval = "30s"
	}
	expectedStatus := strings.TrimSpace(r.FormValue("expected_status"))
	if expectedStatus == "" {
		expectedStatus = "200"
	}
	return &types.HealthCheckSpec{
		Type: "http",
		Params: map[string]string{
			"url":             url,
			"interval":        interval,
			"expected_status": expectedStatus,
		},
	}
}

// sourceFromForm returns the raw source type, a log source and parser when
// both type fields are set, and whether a valid source was parsed.
func sourceFromForm(r *http.Request) (sourceType string, src types.SourceSpec, parser types.ParserSpec, hasSource bool, err error) {
	sourceType = strings.TrimSpace(r.FormValue("source_type"))
	parserType := strings.TrimSpace(r.FormValue("parser_type"))
	if sourceType == "" || parserType == "" {
		return sourceType, types.SourceSpec{}, types.ParserSpec{}, false, nil
	}
	src, err = parseSourceSpec(sourceType, strings.TrimSpace(r.FormValue("source_params")))
	if err != nil {
		return sourceType, types.SourceSpec{}, types.ParserSpec{}, false, err
	}
	return sourceType, src, types.ParserSpec{Type: parserType, Params: parserParamsFromForm(r)}, true, nil
}

// parseSourceSpec builds a SourceSpec from a type and raw key-value
// params text (one "key: value" per line).
func parseSourceSpec(sourceType, paramsRaw string) (types.SourceSpec, error) {
	src := types.SourceSpec{Type: sourceType}
	if paramsRaw != "" {
		params := parseKeyValuePairs(paramsRaw)
		if len(params) > 0 {
			src.Params = params
		}
	}
	return src, nil
}

// parserParamsFromForm reads individual "parser_param_<key>" form fields into
// a params map, skipping empty values.
func parserParamsFromForm(r *http.Request) map[string]string {
	if err := r.ParseForm(); err != nil {
		slog.Error("could not parse form", "error", err)
	}
	params := make(map[string]string)
	for key, values := range r.Form {
		if !strings.HasPrefix(key, "parser_param_") {
			continue
		}
		if v := strings.TrimSpace(values[0]); v != "" {
			params[strings.TrimPrefix(key, "parser_param_")] = v
		}
	}
	return params
}

// parseParserSpec builds a ParserSpec from a type and a params map from the
// form. Params may be JSON-encoded or "key: value" lines.
func parseParserSpec(parserType, parserParamsRaw string) (types.ParserSpec, error) {
	var params map[string]string
	if err := json.Unmarshal([]byte(parserParamsRaw), &params); err != nil {
		params = nil
	}
	if params == nil {
		params = parseKeyValuePairs(parserParamsRaw)
	}
	return types.ParserSpec{
		Type:   parserType,
		Params: params,
	}, nil
}

// parseKeyValuePairs splits "key: value" lines into a map.
func parseKeyValuePairs(raw string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		key := strings.TrimSpace(line[:i])
		val := strings.TrimSpace(line[i+1:])
		if key != "" {
			m[key] = val
		}
	}
	return m
}

// parseFloatPtr parses a form value into a *float64. Empty or invalid
// strings return nil ("use global default").
func parseFloatPtr(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// parseIntPtr parses a form value into a *int. Empty or invalid
// strings return nil ("use global default").
func parseIntPtr(s string) *int {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &v
}
