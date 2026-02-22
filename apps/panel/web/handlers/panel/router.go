package panel

import (
	"embed"
	"net/http"

	"github.com/ai-shift/tracer/apps/panel/internal/services/ingest"
	panelsvc "github.com/ai-shift/tracer/apps/panel/internal/services/panel"
	"github.com/ai-shift/tracer/apps/panel/internal/services/permissions"
	"github.com/ai-shift/tracer/apps/panel/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t             templates.Templates
	panelSvc      *panelsvc.Service
	permissions   *permissions.Service
	ingestService *ingest.Service
	tracker       *ingest.Tracker
}

func InitMux(panelSvc *panelsvc.Service, permissions *permissions.Service, ingestService *ingest.Service, tracker *ingest.Tracker) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:             templates.New(views),
		panelSvc:      panelSvc,
		permissions:   permissions,
		ingestService: ingestService,
		tracker:       tracker,
	}

	// Dashboard
	m.HandleFunc("GET /", h.dashboardPage)
	m.HandleFunc("GET /collectors", h.dashboardCollectors)

	// Collector
	m.HandleFunc("GET /{collector}", h.collectorOverviewPage)
	m.HandleFunc("GET /{collector}/overview", h.collectorOverviewData)
	m.HandleFunc("GET /{collector}/resources", h.collectorResourcesPage)
	m.HandleFunc("GET /{collector}/resources/entries", h.collectorResourcesEntries)
	m.HandleFunc("GET /{collector}/apps", h.collectorAppsPage)
	m.HandleFunc("GET /{collector}/apps/entries", h.collectorAppsEntries)

	// App analytics + logs
	m.HandleFunc("GET /{collector}/apps/{app}", h.appAnalyticsPage)
	m.HandleFunc("GET /{collector}/apps/{app}/logs/recent", h.appRecentLogs)
	m.HandleFunc("GET /{collector}/apps/{app}/logs", h.appLogsPage)
	m.HandleFunc("GET /{collector}/apps/{app}/logs/entries", h.appLogGroupsEntries)
	m.HandleFunc("GET /{collector}/apps/{app}/logs/groups/{groupID}", h.appLogGroupPage)
	m.HandleFunc("GET /{collector}/apps/{app}/logs/groups/{groupID}/entries", h.appLogGroupEntries)
	m.HandleFunc("GET /{collector}/apps/{app}/logs/{id}", h.getLogByID)
	m.HandleFunc("GET /{collector}/apps/{app}/logs/volume", h.appLogsVolume)

	return m
}
