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

	"github.com/ai-shift/tracer/apps/panel/internal/services/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/services/ingest"
	"github.com/ai-shift/tracer/libs/types"
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
	sampleRate    float64
	tracker       *ingest.Tracker
}

func InitMux(authService *auth.Service, ingestService *ingest.Service, sampleRate float64, tracker *ingest.Tracker) *http.ServeMux {
	m := http.NewServeMux()
	h := &handler{
		authService:   authService,
		ingestService: ingestService,
		sampleRate:    sampleRate,
		tracker:       tracker,
	}

	m.HandleFunc("GET /ws/ingest", h.handleWebSocket)
	m.HandleFunc("GET /collectors", h.listCollectors)

	return m
}

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
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Error("WebSocket read error", "error", err)
			}
			break
		}

		h.processMessage(r.Context(), connID, remoteAddr, message)
	}
}

func (h *handler) processMessage(ctx context.Context, connID, remoteAddr string, message []byte) {
	var msg types.LogMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		slog.Error("Failed to parse log message", "error", err)
		return
	}

	// Validate message type
	if err := msg.Type.Validate(); err != nil {
		slog.Warn("Invalid message type", "error", err)
		return
	}

	// Handle each message type with switch case
	switch msg.Type {
	case types.MessageTypeRegister:
		h.handleRegistration(ctx, connID, remoteAddr, msg)

	case types.MessageTypeStats:
		h.handleStats(ctx, msg)

	case types.MessageTypeStartup:
		h.handleStartup(ctx, msg)

	case types.MessageTypeLog:
		h.handleLog(ctx, msg)
	}
}

func (h *handler) handleLog(ctx context.Context, msg types.LogMessage) {
	req := ingest.StoreLogRequest{
		CollectorName: msg.CollectorName,
		AppName:       msg.AppName,
		Entry:         msg.Entry,
		SampleRate:    h.sampleRate,
	}
	if err := req.Validate(); err != nil {
		slog.Warn("Invalid log request", "error", err, "collector", msg.CollectorName, "app", msg.AppName)
		return
	}

	_, err := h.ingestService.StoreLog(ctx, req)
	if err != nil {
		slog.Error("Failed to store log", "error", err, "collector", req.CollectorName, "app", req.AppName)
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

func (h *handler) handleRegistration(ctx context.Context, connID, remoteAddr string, msg types.LogMessage) {
	// Construct request from message
	req := ingest.RegisterCollectorRequest{
		CollectorName: msg.CollectorName,
		Apps:          msg.Apps,
	}

	// Validate request
	if err := req.Validate(); err != nil {
		slog.Warn("Invalid registration request", "error", err, "remote", remoteAddr, "conn_id", connID)
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

func (h *handler) pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
			return
		}
	}
}

// handleStartup processes app startup notifications
// Startup messages are always persisted and trigger a notification
func (h *handler) handleStartup(ctx context.Context, msg types.LogMessage) {
	req := ingest.StoreStartupRequest{
		CollectorName: msg.CollectorName,
		AppName:       msg.AppName,
		Entry:         msg.Entry,
	}
	if err := req.Validate(); err != nil {
		slog.Warn("Invalid startup request", "error", err, "collector", msg.CollectorName, "app", msg.AppName)
		return
	}

	slog.Info("App startup detected", "app", req.AppName, "message", req.Entry.Message)

	if err := h.ingestService.StoreStartup(ctx, req); err != nil {
		slog.Error("Failed to store startup log", "error", err, "app", req.AppName)
	}
}

func (h *handler) listCollectors(w http.ResponseWriter, r *http.Request) {
	if !h.requireCollectorAPIKey(w, r) {
		return
	}

	collectors := h.tracker.List()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(collectors); err != nil {
		slog.Error("Failed to encode collectors", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}

// handleStats processes system statistics from ingests
func (h *handler) handleStats(ctx context.Context, msg types.LogMessage) {
	if msg.Stats == nil {
		slog.Warn("Received stats without stats data")
		return
	}

	ts := msg.Stats.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	// Normalize to UTC for consistent storage
	ts = ts.UTC()

	req := ingest.InsertStatsRequest{
		CollectorName: msg.CollectorName,
		Stats: ingest.SystemStats{
			CollectorName:   msg.CollectorName,
			CpuUsagePercent: msg.Stats.CPUUsagePercent,
			MemTotalMb:      int64(msg.Stats.MemTotalMB),
			MemUsedMb:       int64(msg.Stats.MemUsedMB),
			MemAvailableMb:  int64(msg.Stats.MemAvailableMB),
			DiskTotalGb:     int64(msg.Stats.DiskTotalGB),
			DiskUsedGb:      int64(msg.Stats.DiskUsedGB),
			DiskAvailGb:     int64(msg.Stats.DiskAvailGB),
			CreatedAt:       ts,
		},
	}
	if err := req.Validate(); err != nil {
		slog.Warn("Invalid stats request", "error", err, "collector", msg.CollectorName)
		return
	}

	err := h.ingestService.InsertSystemStats(ctx, req)
	if err != nil {
		slog.Error("Failed to store system stats", "error", err, "collector", msg.CollectorName)
		return
	}

	slog.Debug("Stored system stats", "collector", msg.CollectorName, "cpu", msg.Stats.CPUUsagePercent)
}
