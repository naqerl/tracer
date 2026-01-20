package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ai-shift/tracer/core/services/auth"
	"github.com/ai-shift/tracer/core/services/collectors"
	"github.com/ai-shift/tracer/core/services/dbmanager"
	"github.com/ai-shift/tracer/core/services/notifierstore"
	"github.com/ai-shift/tracer/core/services/notify"
	"github.com/ai-shift/tracer/web/middleware"
	"github.com/ai-shift/tracer/web/resources/analytics"
	"github.com/ai-shift/tracer/web/resources/api"
	authres "github.com/ai-shift/tracer/web/resources/auth"
	"github.com/ai-shift/tracer/web/resources/landing"
	"github.com/ai-shift/tracer/web/resources/llm"
	"github.com/ai-shift/tracer/web/resources/settings"
)

func Run(port string, dbManager *dbmanager.Manager, sampleRate float64, notifyService *notify.Service, authService *auth.Service, notifierStore *notifierstore.Store, tracker *collectors.Tracker, sessionDurationSec int, panelURL string) *http.Server {
	mux := http.NewServeMux()

	// Auth routes (public)
	mux.Handle("/", authres.InitMux(authService, sessionDurationSec))

	// Landing page
	mux.HandleFunc("GET /{$}", landing.InitHandler(authService))
	mux.HandleFunc("GET /index", landing.InitHandler(authService))

	// Documentation routes
	mux.Handle("/llm", llm.InitMux())

	// API routes (uses API key auth via middleware)
	mux.Handle("/api/", http.StripPrefix("/api", api.InitMux(dbManager, authService, sampleRate, notifyService.Chan(), tracker, panelURL)))

	// UI routes (protected by auth middleware)
	mux.Handle("/analytics/", http.StripPrefix("/analytics", analytics.InitMux(dbManager, authService, tracker)))
	mux.Handle("/settings/", http.StripPrefix("/settings", settings.InitMux(authService, notifierStore, dbManager, notifyService)))

	// Apply middleware (order matters: logging → markdown → auth → router)
	markdownMiddleware := middleware.BuildMarkdownMiddleware()
	loginPathMiddleware := middleware.BuildLoginPathMiddleware("/login")
	authMiddleware := middleware.BuildAuthMiddleware(authService)
	handler := loggingMiddleware(markdownMiddleware(loginPathMiddleware(authMiddleware(mux))))

	return &http.Server{
		Addr:         ":" + port,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		slog.Debug("Request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start),
		)
	})
}
