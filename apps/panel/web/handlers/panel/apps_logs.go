package panel

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ai-shift/tracer/apps/panel/internal/services/panelread"
	"github.com/ai-shift/tracer/apps/panel/web/middleware"
	"github.com/ai-shift/tracer/apps/panel/web/templates"
)

func (h *handler) appAnalyticsPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	if middleware.IsMarkdownFormat(r) {
		stats, grouped, err := h.getAppAnalyticsData(r.Context(), collectorName, appName)
		if err != nil {
			h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
			return
		}
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
		"IsAdmin":       user.IsAdmin == 1,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add(collectorName, collectorName).
			Add("apps", "apps").
			Add(appName, appName).
			Build(),
	})
}

func (h *handler) appRecentLogs(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	_, grouped, err := h.getAppAnalyticsData(r.Context(), collectorName, appName)
	if err != nil {
		http.Error(w, "App not found", http.StatusNotFound)
		return
	}

	h.t.RenderHTTP(w, r, "app-logs", map[string]any{
		"AppName":       appName,
		"CollectorName": collectorName,
		"Grouped":       grouped,
	})
}

func (h *handler) appLogsPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	if middleware.IsMarkdownFormat(r) {
		logGroups, err := h.panelRead.GetLogGroups(r.Context(), collectorName, appName, "", 0, 50)
		if err != nil {
			if err == panelread.ErrAppNotFound {
				h.t.RenderError(w, r, "Error", "App not found", http.StatusNotFound)
				return
			}
			h.t.RenderError(w, r, "Error", "Failed to fetch log groups", http.StatusInternalServerError)
			return
		}
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
		"User":          user,
		"IsAdmin":       user.IsAdmin == 1,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add(collectorName, collectorName).
			Add("apps", "apps").
			Add(appName, appName).
			Add("logs", "logs").
			Build(),
	})
}

func (h *handler) appLogGroupsEntries(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	level := r.URL.Query().Get("level")
	offset := parseOffset(r.URL.Query().Get("offset"))
	limit := int64(50)
	logGroups, err := h.panelRead.GetLogGroups(r.Context(), collectorName, appName, level, offset, limit)
	if err != nil {
		if err == panelread.ErrAppNotFound {
			http.Error(w, "App not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to fetch log groups", http.StatusInternalServerError)
		return
	}

	currentCount := logGroups.TotalCount
	if level != "" {
		switch level {
		case "error":
			currentCount = logGroups.ErrorCount
		case "warn":
			currentCount = logGroups.WarnCount
		case "info":
			currentCount = logGroups.InfoCount
		}
	}

	h.t.RenderHTTP(w, r, "app-logs-table", map[string]any{
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
		"HasMore":       offset+limit < currentCount,
		"NextOffset":    offset + limit,
	})
}

func (h *handler) appLogGroupPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Invalid group ID", http.StatusBadRequest)
		return
	}

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this app", http.StatusForbidden)
		return
	}

	detail, err := h.panelRead.GetLogDetail(r.Context(), collectorName, appName, groupID, 0, 1)
	if err != nil {
		if err == panelread.ErrAppNotFound || err == panelread.ErrLogNotFound {
			h.t.RenderError(w, r, "Error", "Group not found", http.StatusNotFound)
			return
		}
		h.t.RenderError(w, r, "Error", "Failed to fetch logs", http.StatusInternalServerError)
		return
	}

	highlightLogID := parseOffset(r.URL.Query().Get("logID"))

	h.t.RenderHTTP(w, r, "app-logs-detail-page", map[string]any{
		"Title":          appName + " Log Details",
		"AppName":        appName,
		"CollectorName":  collectorName,
		"LogGroup":       detail.Group,
		"HighlightLogID": highlightLogID,
		"User":           user,
		"IsAdmin":        user.IsAdmin == 1,
		"Nav": templates.NewNavBuilder().
			Add("panel", "panel").
			Add(collectorName, collectorName).
			Add("apps", "apps").
			Add(appName, appName).
			Add("logs", "logs").
			Build(),
	})
}

func (h *handler) appLogGroupEntries(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	groupID, err := strconv.ParseInt(r.PathValue("groupID"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid group ID", http.StatusBadRequest)
		return
	}

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	offset := parseOffset(r.URL.Query().Get("offset"))
	highlightLogID := parseOffset(r.URL.Query().Get("logID"))
	limit := int64(50)
	detail, err := h.panelRead.GetLogDetail(r.Context(), collectorName, appName, groupID, offset, limit)
	if err != nil {
		if err == panelread.ErrAppNotFound || err == panelread.ErrLogNotFound {
			http.Error(w, "Group not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to fetch logs", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "app-logs-detail", map[string]any{
		"AppName":        appName,
		"CollectorName":  collectorName,
		"LogGroup":       detail.Group,
		"Logs":           detail.Logs,
		"Offset":         offset,
		"Limit":          limit,
		"HasMore":        offset+int64(len(detail.Logs)) < detail.TotalCount,
		"NextOffset":     offset + limit,
		"HighlightLogID": highlightLogID,
	})
}

func (h *handler) appLogsVolume(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
	if err != nil {
		http.Error(w, "Failed to check access", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	timeSeriesData, err := h.panelRead.GetLogVolumeTimeSeries(r.Context(), collectorName, appName, parseTimeRange(rangeParam), getBucketGranularity(rangeParam))
	if err != nil {
		if err == panelread.ErrAppNotFound {
			http.Error(w, "App not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to fetch chart data", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "app-logs-chart-data", map[string]any{
		"AppName":       appName,
		"CollectorName": collectorName,
		"TimeSeries":    timeSeriesData,
		"RangeParam":    rangeParam,
	})
}

func (h *handler) getLogByID(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorName := r.PathValue("collector")
	appName := r.PathValue("app")
	idStr := r.PathValue("id")

	hasAccess, err := h.permissions.CanAccessApp(r.Context(), user, appName)
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

	logEntry, err := h.panelRead.GetLogByID(r.Context(), collectorName, appName, id)
	if err != nil {
		if err == panelread.ErrAppNotFound {
			http.Error(w, "App not found", http.StatusNotFound)
			return
		}
		if err == panelread.ErrLogNotFound {
			http.Error(w, "Log not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Failed to get log", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/panel/%s/apps/%s/logs/groups/%d?logID=%d", collectorName, appName, logEntry.GroupID.Int64, logEntry.ID), http.StatusSeeOther)
}

func (h *handler) getAppAnalyticsData(ctx context.Context, collectorName, appName string) (*panelread.AppStats, any, error) {
	stats, grouped, err := h.panelRead.GetAppAnalytics(ctx, collectorName, appName)
	if err != nil {
		if err == panelread.ErrAppNotFound {
			return nil, nil, fmt.Errorf("app not found")
		}
		return nil, nil, err
	}
	return stats, grouped, nil
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
