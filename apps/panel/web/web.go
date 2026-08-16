package web

import (
	"log/slog"
	"net/http"
	"time"

	"tracer/apps/panel/web/handlers/auth"
	"tracer/apps/panel/web/handlers/ingest"
	"tracer/apps/panel/web/handlers/llm"
	"tracer/apps/panel/web/handlers/panel"
	"tracer/apps/panel/web/handlers/settings"
	"tracer/apps/panel/web/handlers/topology"
	"tracer/apps/panel/web/middleware"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/static"
)

// NewServer creates and configures an HTTP server with all handlers.
func NewServer(deps Dependencies, port string, noAuth bool, readOnly bool) *http.Server {
	mux := http.NewServeMux()
	authMux := auth.InitMux(deps.Auth, deps.User)

	// Health check endpoint for k8s/load balancer probes (no auth required)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if middleware.HasUserID(r) {
			http.Redirect(w, r, "/panel/", http.StatusSeeOther)
		} else {
			authmw.RedirectToLogin(w, r)
		}
	})
	mux.Handle("/static/", http.StripPrefix("/static", static.InitMux()))
	mux.Handle("/ingest/", http.StripPrefix("/ingest", ingest.InitMux(deps.Auth, deps.Ingest, deps.Tracker, deps.Topology)))
	mux.Handle("/panel/", http.StripPrefix("/panel", panel.InitMux(deps.PanelService)))
	mux.Handle("/topology/", http.StripPrefix("/topology", topology.InitMux(deps.Topology, deps.User)))
	mux.Handle("/auth/", http.StripPrefix("/auth", authMux))
	mux.Handle("/llm", llm.InitMux())
	mux.Handle("/settings/", http.StripPrefix("/settings", settings.InitMux(deps.Auth, deps.User, deps.Ingest, deps.DBManagerx, deps.Notify, deps.Config, readOnly)))

	markdownMiddleware := middleware.BuildMarkdownMiddleware()
	loginPathMiddleware := authmw.BuildLoginPathMiddleware("/auth/login")
	authMiddleware := authmw.BuildAuthMiddleware(deps.Auth, deps.User)

	var handler http.Handler = mux

	if readOnly {
		handler = middleware.BuildReadOnlyMiddleware()(handler)
		slog.Info("read-only mode enabled")
	}

	handler = authMiddleware(handler)
	handler = loginPathMiddleware(handler)
	handler = markdownMiddleware(handler)
	handler = middleware.BuildMarkdownAcceptMiddleware()(handler)

	if noAuth {
		handler = middleware.BuildNoAuthMiddleware(deps.User)(handler)
		slog.Warn("no-auth mode enabled")
	}

	handler = middleware.LoggingMiddleware(handler)

	return &http.Server{
		Addr:        ":" + port,
		Handler:     handler,
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
	}
}
