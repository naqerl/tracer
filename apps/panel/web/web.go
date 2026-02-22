package web

import (
	"net/http"
	"time"

	authres "github.com/ai-shift/tracer/apps/panel/web/handlers/auth"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/ingest"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/llm"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/panel"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/settings"
	"github.com/ai-shift/tracer/apps/panel/web/middleware"
)

func Run(deps Dependencies, port string, sampleRate float64, panelURL string) *http.Server {
	mux := http.NewServeMux()

	mux.Handle("/ingest/", http.StripPrefix("/ingest", ingest.InitMux(deps.Ingest, sampleRate, deps.Notify.Chan(), deps.Tracker, panelURL)))
	mux.Handle("/panel/", http.StripPrefix("/panel", panel.InitMux(deps.DBManagerx, deps.Auth, deps.User, deps.Ingest, deps.Tracker)))
	mux.Handle("/auth/", http.StripPrefix("/auth", authres.InitMux(deps.Auth, deps.User)))
	mux.Handle("/llm", llm.InitMux())
	mux.Handle("/settings/", http.StripPrefix("/settings", settings.InitMux(deps.Auth, deps.User, deps.Ingest, deps.DBManagerx, deps.Notify)))

	markdownMiddleware := middleware.BuildMarkdownMiddleware()
	loginPathMiddleware := middleware.BuildLoginPathMiddleware("/login")
	authMiddleware := middleware.BuildAuthMiddleware(deps.Auth, deps.User)
	handler := middleware.LoggingMiddleware(markdownMiddleware(loginPathMiddleware(authMiddleware(mux))))

	return &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}
