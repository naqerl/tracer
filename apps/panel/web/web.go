package web

import (
	"net/http"
	"time"

	panelsvc "tracer/apps/panel/internal/services/panel"
	"tracer/apps/panel/internal/services/permissions"

	authres "tracer/apps/panel/web/handlers/auth"
	"tracer/apps/panel/web/handlers/ingest"
	"tracer/apps/panel/web/handlers/landing"
	"tracer/apps/panel/web/handlers/llm"
	"tracer/apps/panel/web/handlers/panel"
	"tracer/apps/panel/web/handlers/settings"
	"tracer/apps/panel/web/middleware"
	authmw "tracer/apps/panel/web/middleware/auth"
)

func Run(deps Dependencies, port string, sampleRate float64) *http.Server {
	mux := http.NewServeMux()
	permissionService := permissions.New(deps.User, deps.Tracker)
	panelService := panelsvc.New(deps.DBManagerx, permissionService, deps.Ingest, deps.Tracker)
	authMux := authres.InitMux(deps.Auth, deps.User)
	panelMux := panel.InitMux(panelService)

	mux.Handle("/ingest/", http.StripPrefix("/ingest", ingest.InitMux(deps.Auth, deps.Ingest, sampleRate, deps.Tracker)))
	mux.Handle("/panel/", http.StripPrefix("/panel", panelMux))
	mux.Handle("/auth/", http.StripPrefix("/auth", authMux))
	mux.Handle("/llm", llm.InitMux())
	mux.Handle("/settings/", http.StripPrefix("/settings", settings.InitMux(deps.Auth, deps.User, deps.Ingest, deps.DBManagerx, deps.Notify, permissionService)))
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { authMux.ServeHTTP(w, r) })
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) { authMux.ServeHTTP(w, r) })
	mux.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) { authMux.ServeHTTP(w, r) })
	mux.HandleFunc("GET /setup", func(w http.ResponseWriter, r *http.Request) { authMux.ServeHTTP(w, r) })
	mux.HandleFunc("POST /setup", func(w http.ResponseWriter, r *http.Request) { authMux.ServeHTTP(w, r) })
	mux.Handle("/", landing.InitHandler())

	markdownMiddleware := middleware.BuildMarkdownMiddleware()
	loginPathMiddleware := authmw.BuildLoginPathMiddleware("/login")
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
