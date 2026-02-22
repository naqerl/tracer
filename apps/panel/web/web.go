package web

import (
	"net/http"
	"time"

	"github.com/ai-shift/tracer/apps/panel/internal/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/collectors"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/apps/panel/internal/notify"
	"github.com/ai-shift/tracer/apps/panel/internal/user"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/analytics"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/api"
	authres "github.com/ai-shift/tracer/apps/panel/web/handlers/auth"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/llm"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/settings"
	"github.com/ai-shift/tracer/apps/panel/web/middleware"
)

func Run(port string, dbManager *dbmanager.Manager, collectorService *collectors.Service, sampleRate float64, notifyService *notify.Service, authService *auth.Service, userService *user.Service, tracker *collectors.Tracker, panelURL string) *http.Server {
	mux := http.NewServeMux()

	mux.Handle("/", authres.InitMux(authService, userService))
	mux.Handle("/llm", llm.InitMux())
	mux.Handle("/ingest/", http.StripPrefix("/ingest", api.InitMux(collectorService, sampleRate, notifyService.Chan(), tracker, panelURL)))
	mux.Handle("/analytics/", http.StripPrefix("/analytics", analytics.InitMux(dbManager, authService, userService, collectorService, tracker)))
	mux.Handle("/settings/", http.StripPrefix("/settings", settings.InitMux(authService, userService, collectorService, dbManager, notifyService)))

	markdownMiddleware := middleware.BuildMarkdownMiddleware()
	loginPathMiddleware := middleware.BuildLoginPathMiddleware("/login")
	authMiddleware := middleware.BuildAuthMiddleware(authService, userService)
	handler := middleware.LoggingMiddleware(markdownMiddleware(loginPathMiddleware(authMiddleware(mux))))

	return &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

