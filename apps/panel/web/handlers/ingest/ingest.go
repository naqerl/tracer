package ingest

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/libs/types"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Configure for production
	},
}

type handler struct {
	authService   *auth.Service
	ingestService *ingest.Service
	tracker       *ingest.Tracker
}

func InitMux(authService *auth.Service, ingestService *ingest.Service, tracker *ingest.Tracker) *http.ServeMux {
	m := http.NewServeMux()
	h := &handler{
		authService:   authService,
		ingestService: ingestService,
		tracker:       tracker,
	}

	m.HandleFunc("GET /ws", h.handleWebSocket)

	return m
}

// Handlers

func (h *handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !h.requireCollectorAPIKey(w, r) {
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	// Generate unique connection ID
	connID := uuid.New().String()
	remoteAddr := extractIP(r.RemoteAddr)

	slog.Info("WebSocket connected", "remote", remoteAddr, "conn_id", connID)

	// Configure connection with generous timeout
	conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		h.tracker.UpdateLastSeen(connID)
		return nil
	})

	// Mark as disconnected when connection closes
	defer func() {
		h.tracker.Disconnect(connID)
		slog.Info("WebSocket disconnected", "remote", remoteAddr, "conn_id", connID)
	}()

	// Start ping ticker
	go h.pingLoop(conn)

	// Read loop
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Debug("WebSocket closed", "error", err)
			} else if websocket.IsUnexpectedCloseError(err, websocket.CloseAbnormalClosure) {
				slog.Error("WebSocket read error", "error", err)
			} else {
				slog.Warn("WebSocket read closed", "error", err)
			}
			break
		}

		h.processMessage(r.Context(), connID, remoteAddr, message)
	}
}

// Helpers

func (h *handler) processMessage(ctx context.Context, connID, remoteAddr string, message []byte) {
	var hdr struct {
		Type types.EventType `json:"type"`
	}
	if err := json.Unmarshal(message, &hdr); err != nil {
		slog.Error("Failed to parse websocket message header", "error", err)
		return
	}
	if err := hdr.Type.Validate(); err != nil {
		slog.Error("Invalid websocket message type", "error", err)
		return
	}

	switch hdr.Type {
	case types.EventTypeRegister:
		var reg types.RegisterMessage
		if err := json.Unmarshal(message, &reg); err != nil {
			slog.Error("Failed to parse collector registration", "error", err)
			return
		}
		h.handleRegistration(ctx, connID, remoteAddr, reg)
	default:
		var env types.Envelope
		if err := json.Unmarshal(message, &env); err != nil {
			slog.Error("Failed to parse ingest envelope", "error", err)
			return
		}
		h.handleEnvelope(ctx, env)
	}
}

func (h *handler) handleEnvelope(ctx context.Context, env types.Envelope) {
	switch env.Type {
	case types.EventTypeLog:
		var evt types.LogEvent
		if err := json.Unmarshal(env.Data, &evt); err != nil {
			slog.Warn("Failed to decode log event", "error", err, "collector", env.Collector)
			return
		}
		h.handleLog(ctx, env.Collector, evt.App, evt.Entry)
	case types.EventTypeLifecycle:
		var evt types.LifecycleEvent
		if err := json.Unmarshal(env.Data, &evt); err != nil {
			slog.Warn("Failed to decode lifecycle event", "error", err, "collector", env.Collector)
			return
		}
		h.handleLifecycle(ctx, env.Collector, evt)
	case types.EventTypeStats:
		var evt types.StatsEvent
		if err := json.Unmarshal(env.Data, &evt); err != nil {
			slog.Warn("Failed to decode stats event", "error", err, "collector", env.Collector)
			return
		}
		h.handleStats(ctx, env.Collector, evt.Data)
	case types.EventTypeError:
		var evt types.ErrorEvent
		if err := json.Unmarshal(env.Data, &evt); err != nil {
			slog.Warn("Failed to decode error event", "error", err, "collector", env.Collector)
			return
		}
		entry := types.LogEntry{
			Timestamp: evt.Timestamp(),
			Level:     types.LevelError,
			Message:   "SOURCE_ERROR[" + evt.ErrType + "]: " + evt.Message + " (source: " + evt.Source + ")",
		}
		h.handleLog(ctx, env.Collector, evt.App, entry)
	case types.EventTypeHealth:
		var evt types.HealthEvent
		if err := json.Unmarshal(env.Data, &evt); err != nil {
			slog.Warn("Failed to decode health event", "error", err, "collector", env.Collector)
			return
		}
		h.handleHealth(ctx, env.Collector, evt)
	default:
		slog.Warn("Unhandled envelope type", "type", env.Type)
	}
}

func (h *handler) handleRegistration(ctx context.Context, connID, remoteAddr string, msg types.RegisterMessage) {
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

func (h *handler) handleLog(ctx context.Context, collector, app string, entry types.LogEntry) {
	req := ingest.StoreLogRequest{
		CollectorName: collector,
		AppName:       app,
		Entry:         entry,
	}
	if err := req.Validate(); err != nil {
		slog.Error("Invalid log request", "error", err, "collector", collector, "app", app)
		return
	}

	_, err := h.ingestService.StoreLog(ctx, req)
	if err != nil {
		slog.Error("Failed to store log", "error", err, "collector", req.CollectorName, "app", req.AppName)
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
	}
}

func (h *handler) pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
			return
		}
	}
}

func (h *handler) requireCollectorAPIKey(w http.ResponseWriter, r *http.Request) bool {
	apiKey := r.Header.Get("X-API-Key")
	if apiKey == "" {
		http.Error(w, "API key required", http.StatusUnauthorized)
		return false
	}

	valid, err := h.authService.ValidateAPIKey(r.Context(), apiKey)
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
