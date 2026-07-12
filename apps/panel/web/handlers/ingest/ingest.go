package ingest

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	topologysvc "tracer/apps/panel/internal/services/topology"
	"tracer/libs/types"
	"tracer/libs/wsx"
)

type handler struct {
	authService   *auth.Service
	ingestService *ingest.Service
	tracker       *ingest.Tracker
	topology      *topologysvc.Service
}

func InitMux(authService *auth.Service, ingestService *ingest.Service, tracker *ingest.Tracker, topology *topologysvc.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := &handler{
		authService:   authService,
		ingestService: ingestService,
		tracker:       tracker,
		topology:      topology,
	}

	m.HandleFunc("GET /ws", h.handleWebSocket)

	return m
}

// Handlers

func (h *handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !h.requireCollectorAPIKey(w, r) {
		return
	}

	connID := uuid.New().String()
	remoteAddr := extractIP(r.RemoteAddr)

	client, err := wsx.Upgrade(r.Context(), w, r,
		wsx.WithReadLimit(10*1024*1024),
		wsx.WithPongWait(5*time.Minute),
		wsx.WithPongCallback(func() {
			h.tracker.UpdateLastSeen(connID)
		}),
		wsx.WithWriteTimeout(10*time.Second),
	)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}

	slog.Info("WebSocket connected", "remote", remoteAddr, "conn_id", connID)

	defer func() {
		client.Close()
		h.tracker.Disconnect(connID)
		slog.Info("WebSocket disconnected", "remote", remoteAddr, "conn_id", connID)
	}()

	// Process events from Recv until the channel is closed.
	for evt := range client.Recv() {
		switch e := evt.(type) {
		case types.RegisterEvent:
			h.topology.Publish(topologysvc.Message{
				Register: &e,
			})
			h.handleRegistration(r.Context(), connID, remoteAddr, e)
		case types.Envelope:
			h.handleEnvelope(r.Context(), e)
		default:
			slog.Warn("Unknown event type from Recv", "event_type", evt.EventType())
		}
	}
}

// Helpers

func (h *handler) handleEnvelope(ctx context.Context, env types.Envelope) {
	switch env.Type {
	case types.EventTypeLog:
		var evt types.LogEvent
		if err := env.DecodeData(&evt); err != nil {
			slog.Warn("Failed to decode log event", "error", err, "collector", env.Collector)
			return
		}
		h.handleLog(ctx, env.Collector, evt.App, evt.Entry, &env)
	case types.EventTypeLifecycle:
		var evt types.LifecycleEvent
		if err := env.DecodeData(&evt); err != nil {
			slog.Warn("Failed to decode lifecycle event", "error", err, "collector", env.Collector)
			return
		}
		h.handleLifecycle(ctx, env.Collector, evt)
	case types.EventTypeStats:
		h.topology.Publish(topologysvc.Message{
			Envelope: &env,
		})
		var evt types.StatsEvent
		if err := env.DecodeData(&evt); err != nil {
			slog.Warn("Failed to decode stats event", "error", err, "collector", env.Collector)
			return
		}
		h.handleStats(ctx, env.Collector, evt.Data)
	case types.EventTypeError:
		var evt types.ErrorEvent
		if err := env.DecodeData(&evt); err != nil {
			slog.Warn("Failed to decode error event", "error", err, "collector", env.Collector)
			return
		}
		entry := types.LogEntry{
			Timestamp: evt.Timestamp(),
			Level:     types.LevelError,
			Message:   "SOURCE_ERROR[" + evt.ErrType + "]: " + evt.Message + " (source: " + evt.Source + ")",
		}
		h.handleLog(ctx, env.Collector, evt.App, entry, nil)
	case types.EventTypeHealth:
		h.topology.Publish(topologysvc.Message{
			Envelope: &env,
		})
		var evt types.HealthEvent
		if err := env.DecodeData(&evt); err != nil {
			slog.Warn("Failed to decode health event", "error", err, "collector", env.Collector)
			return
		}
		h.handleHealth(ctx, env.Collector, evt)
	default:
		slog.Error("Unhandled envelope type", "type", env.Type)
	}
}

func (h *handler) handleRegistration(ctx context.Context, connID, remoteAddr string, msg types.RegisterEvent) {
	if len(msg.Apps) == 0 {
		slog.Info("Skipping empty collector registration", "collector", msg.Collector, "remote", remoteAddr, "conn_id", connID)
		return
	}

	// Construct request from message
	req := ingest.RegisterCollectorRequest{
		CollectorName: msg.Collector,
		Apps:          msg.Apps,
	}

	// Validate request
	if err := req.Validate(); err != nil {
		slog.Error("Invalid registration request", "error", err, "remote", remoteAddr, "conn_id", connID)
		return
	}

	slog.Info("Received app registration", "ingest", req.CollectorName, "remote", remoteAddr, "apps", req.Apps, "count", len(req.Apps))

	// Register ingest with tracker
	h.tracker.Register(connID, req.CollectorName, remoteAddr, req.Apps)

	// Create all databases via ingest service
	if err := h.ingestService.RegisterCollector(ctx, req); err != nil {
		slog.Error("Failed to register collector", "collector", req.CollectorName, "error", err)
		return
	}

	for _, appName := range req.Apps {
		if appName != "" {
			slog.Info("Registered app", "app_name", appName)
		}
	}
}

func (h *handler) handleLifecycle(ctx context.Context, collector string, evt types.LifecycleEvent) {
	req := ingest.StoreLifecycleRequest{
		CollectorName: collector,
		AppName:       evt.App,
		Kind:          evt.Kind,
		RawLine:       evt.RawLine,
		Timestamp:     evt.Timestamp(),
	}
	if err := req.Validate(); err != nil {
		slog.Error("Invalid lifecycle request", "error", err, "collector", collector, "app", evt.App)
		return
	}

	if err := h.ingestService.StoreLifecycle(ctx, req); err != nil {
		slog.Error("Failed to store lifecycle event", "error", err, "collector", collector, "app", evt.App, "kind", evt.Kind)
	}
}

func (h *handler) handleLog(ctx context.Context, collector, app string, entry types.LogEntry, env *types.Envelope) {
	req := ingest.StoreLogRequest{
		CollectorName: collector,
		AppName:       app,
		Entry:         entry,
	}
	if err := req.Validate(); err != nil {
		slog.Error("Invalid log request", "error", err, "collector", collector, "app", app)
		return
	}

	logID, err := h.ingestService.StoreLog(ctx, req)
	if err != nil {
		slog.Error("Failed to store log", "error", err, "collector", req.CollectorName, "app", req.AppName)
		return
	}

	if logID > 0 {
		msg := topologysvc.Message{
			Persisted: &topologysvc.PersistedLogMessage{
				Collector: collector,
				App:       app,
				Level:     string(entry.Level),
				Timestamp: entry.Timestamp.Format(time.RFC3339Nano),
			},
		}
		if env != nil {
			msg.Envelope = env
		}
		h.topology.Publish(msg)
	}
}

// handleStats processes system statistics from collectors.
func (h *handler) handleStats(ctx context.Context, collector string, stats types.Stats) {
	req := ingest.SaveSystemStatsRequest{
		CollectorName: collector,
		Stats:         stats,
	}
	if err := req.Validate(); err != nil {
		slog.Error("Invalid system stats request", "error", err, "collector", collector)
		return
	}

	if err := h.ingestService.SaveSystemStats(ctx, req); err != nil {
		slog.Error("Failed to save system stats", "error", err, "collector", collector)
		return
	}

	slog.Debug("Saved system stats", "collector", collector, "cpu", stats.CPUUsagePercent)
}

func (h *handler) handleHealth(ctx context.Context, collector string, evt types.HealthEvent) {
	req := ingest.StoreHealthRequest{
		CollectorName: collector,
		AppName:       evt.App,
		Status:        evt.Status,
		Time:          evt.Time,
	}
	if err := req.Validate(); err != nil {
		slog.Error("Invalid health request", "error", err, "collector", collector, "app", evt.App)
		return
	}

	if err := h.ingestService.SaveHealthPeriod(ctx, req); err != nil {
		slog.Error("Failed to store health event", "error", err, "collector", collector, "app", evt.App)
		return
	}

}

func (h *handler) requireCollectorAPIKey(w http.ResponseWriter, r *http.Request) bool {
	apiKey := r.Header.Get("X-API-Key")
	if apiKey == "" {
		http.Error(w, "API key required", http.StatusUnauthorized)
		return false
	}

	valid, err := h.authService.ValidateCollectorAPIKey(r.Context(), apiKey)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return false
	}
	if !valid {
		http.Error(w, "Invalid API key", http.StatusUnauthorized)
		return false
	}
	return true
}

// extractIP extracts the IP address from remote address (removes port)
func extractIP(remoteAddr string) string {
	// Handle IPv6 addresses like [::1]:port
	if idx := strings.LastIndex(remoteAddr, ":"); idx != -1 {
		ip := remoteAddr[:idx]
		// Remove brackets from IPv6
		ip = strings.TrimPrefix(ip, "[")
		ip = strings.TrimSuffix(ip, "]")
		return ip
	}
	return remoteAddr
}
