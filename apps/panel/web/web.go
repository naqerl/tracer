package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ai-shift/tracer/apps/panel/internal/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/collectors"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/apps/panel/internal/notify"
	"github.com/ai-shift/tracer/apps/panel/internal/user"
	"github.com/ai-shift/tracer/apps/panel/web/middleware"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/analytics"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/api"
	authres "github.com/ai-shift/tracer/apps/panel/web/handlers/auth"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/landing"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/llm"
	"github.com/ai-shift/tracer/apps/panel/web/handlers/settings"
)

func Run(port string, dbManager *dbmanager.Manager, collectorService *collectors.Service, sampleRate float64, notifyService *notify.Service, authService *auth.Service, userService *user.Service, notifierStore *notify.Store, tracker *collectors.Tracker, sessionDurationSec int, panelURL string) *http.Server {
	mux := http.NewServeMux()

	// Auth routes (public)
	mux.Handle("/", authres.InitMux(authService, userService, sessionDurationSec))

	// Landing page
	mux.HandleFunc("GET /{$}", landing.InitHandler())
	mux.HandleFunc("GET /index", landing.InitHandler())

	// Documentation routes
	mux.Handle("/llm", llm.InitMux())

	// API routes (uses API key auth via middleware)
	mux.Handle("/api/", http.StripPrefix("/api", api.InitMux(dbManager, collectorService, sampleRate, notifyService.Chan(), tracker, panelURL)))

	// UI routes (protected by auth middleware)
	mux.Handle("/analytics/", http.StripPrefix("/analytics", analytics.InitMux(dbManager, authService, userService, collectorService, tracker)))
	mux.Handle("/settings/", http.StripPrefix("/settings", settings.InitMux(authService, userService, collectorService, notifierStore, dbManager, notifyService)))

	// Apply middleware (order matters: logging → markdown → auth → router)
	markdownMiddleware := middleware.BuildMarkdownMiddleware()
	loginPathMiddleware := middleware.BuildLoginPathMiddleware("/login")
	authMiddleware := middleware.BuildAuthMiddleware(authService, userService)
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

