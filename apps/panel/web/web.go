package web

import (
	"net/http"
	"time"

	"tracer/apps/panel/web/handlers/auth"
	"tracer/apps/panel/web/handlers/ingest"
	"tracer/apps/panel/web/handlers/llm"
	"tracer/apps/panel/web/handlers/panel"
	"tracer/apps/panel/web/handlers/settings"
	"tracer/apps/panel/web/middleware"
	authmw "tracer/apps/panel/web/middleware/auth"
)

// NewServer creates and configures an HTTP server with all handlers.
func NewServer(deps Dependencies, port string, sampleRate float64) *http.Server {
	mux := http.NewServeMux()
	authMux := auth.InitMux(deps.Auth, deps.User)

	mux.Handle("/ingest/", http.StripPrefix("/ingest", ingest.InitMux(deps.Auth, deps.Ingest, sampleRate, deps.Tracker)))
	mux.Handle("/panel/", http.StripPrefix("/panel", panel.InitMux(deps.PanelService)))
	mux.Handle("/auth/", http.StripPrefix("/auth", authMux))
	mux.Handle("/llm", llm.InitMux())
	mux.Handle("/settings/", http.StripPrefix("/settings", settings.InitMux(deps.Auth, deps.User, deps.Ingest, deps.DBManagerx, deps.Notify)))

	markdownMiddleware := middleware.BuildMarkdownMiddleware()
	loginPathMiddleware := authmw.BuildLoginPathMiddleware("/auth/login")
	authMiddleware := authmw.BuildAuthMiddleware(deps.Auth, deps.User)
	handler := middleware.LoggingMiddleware(markdownMiddleware(loginPathMiddleware(authMiddleware(mux))))

	return &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}
