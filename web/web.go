package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/ai-shift/tracer/core/services/alerter"
	"github.com/ai-shift/tracer/core/services/dbmanager"
	"github.com/ai-shift/tracer/web/resources/analytics"
	"github.com/ai-shift/tracer/web/resources/api"
	"github.com/ai-shift/tracer/web/resources/logs"
)

func Run(port string, dbManager *dbmanager.Manager, sampleRate float64, alertChan chan<- alerter.Alert) *http.Server {
	mux := http.NewServeMux()

	// Root redirects to analytics dashboard (must be registered first with exact path)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/analytics/", http.StatusTemporaryRedirect)
	})

	// API routes (WebSocket ingest)
	mux.Handle("/api/", http.StripPrefix("/api", api.InitMux(dbManager, sampleRate, alertChan)))

	// UI routes
	mux.Handle("/logs/", http.StripPrefix("/logs", logs.InitMux(dbManager)))
	mux.Handle("/analytics/", http.StripPrefix("/analytics", analytics.InitMux(dbManager)))

	// Apply middleware
	handler := loggingMiddleware(mux)

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
