package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/ai-shift/tracer/apps/panel/internal/services/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/services/ingest"
	"github.com/ai-shift/tracer/apps/panel/internal/services/notify"
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
	notifyChan    notify.NotificationChan
	tracker       *ingest.Tracker
	panelURL      string
}

func InitMux(authService *auth.Service, ingestService *ingest.Service, sampleRate float64, notifyChan notify.NotificationChan, tracker *ingest.Tracker, panelURL string) *http.ServeMux {
	m := http.NewServeMux()
	h := &handler{
		authService:   authService,
		ingestService: ingestService,
		sampleRate:    sampleRate,
		notifyChan:    notifyChan,
		tracker:       tracker,
		panelURL:      panelURL,
	}

	m.HandleFunc("GET /ws/ingest", h.handleWebSocket)
	m.HandleFunc("GET /collectors", h.listCollectors)

	return m
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

func (h *handler) handleRegistration(ctx context.Context, connID, remoteAddr, ingestName string, apps []string) {
	if ingestName == "" {
		slog.Warn("Registration rejected: missing ingest_name", "remote", remoteAddr, "conn_id", connID)
		return
	}

	slog.Info("Received app registration", "ingest", ingestName, "remote", remoteAddr, "apps", apps, "count", len(apps))

	// Register ingest with tracker
	h.tracker.Register(connID, ingestName, remoteAddr, apps)

	// Create all databases via ingest service
	if err := h.ingestService.Register(ingestName, apps); err != nil {
		slog.Error("Failed to register ingest", "ingest", ingestName, "error", err)
		return
	}

	for _, appName := range apps {
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

func (h *handler) processMessage(ctx context.Context, connID, remoteAddr string, message []byte) {
	var msg types.LogMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		slog.Error("Failed to parse log message", "error", err)
		return
	}

	// Handle registration messages
	if msg.Type == "register" {
		h.handleRegistration(ctx, connID, remoteAddr, msg.CollectorName, msg.Apps)
		return
	}

	// Update last seen on any message
	h.tracker.UpdateLastSeen(connID)

	// Handle stats messages
	if msg.Type == "stats" {
		h.handleStats(ctx, msg)
		return
	}

	// Handle startup messages - always persist and notify
	if msg.Type == "startup" {
		h.handleStartup(ctx, msg)
		return
	}

	// Handle log messages
	if msg.AppName == "" {
		slog.Warn("Received log without app_name")
		return
	}
	if msg.CollectorName == "" {
		slog.Warn("Received log without collector_name", "app_name", msg.AppName)
		return
	}

	// Sampling logic: always persist errors, sample others
	isError := msg.Entry.Level == "error" || msg.Entry.Level == "ERROR"
	shouldPersist := isError || rand.Float64() < h.sampleRate

	if !shouldPersist {
		return
	}

	// Store log via ingest service
	if err := h.ingestService.StoreLog(ctx, msg.CollectorName, msg.AppName, msg.Entry); err != nil {
		slog.Error("Failed to store log", "error", err, "app_name", msg.AppName)
		return
	}

	// Send notification for errors
	if isError && h.notifyChan != nil {
		// Build the alert message with markdown link
		alertMessage := fmt.Sprintf("```%s```", msg.Entry.Message)
		if h.panelURL != "" {
			logsURL := fmt.Sprintf("%s/analytics/%s/apps/%s/logs",
				h.panelURL,
				msg.CollectorName,
				msg.AppName)
			alertMessage += fmt.Sprintf("\n\n[view logs](%s)", logsURL)
		}

		select {
		case h.notifyChan <- notify.Notification{
			Type:          notify.TypeAlert,
			CollectorName: msg.CollectorName,
			AppName:       msg.AppName,
			Message:       alertMessage,
		}:
		default:
			slog.Warn("Notification channel full, dropping notification")
		}
	}
}

// handleStartup processes app startup notifications
// Startup messages are always persisted and trigger a notification
func (h *handler) handleStartup(ctx context.Context, msg types.LogMessage) {
	if msg.AppName == "" {
		slog.Warn("Received startup without app_name")
		return
	}
	if msg.CollectorName == "" {
		slog.Warn("Received startup without collector_name", "app_name", msg.AppName)
		return
	}

	slog.Info("App startup detected", "app", msg.AppName, "message", msg.Entry.Message)

	// Store startup via ingest service
	if err := h.ingestService.StoreStartup(ctx, msg.CollectorName, msg.AppName, msg.Entry); err != nil {
		slog.Error("Failed to store startup log", "error", err, "app_name", msg.AppName)
		return
	}

	// Send startup notification
	if h.notifyChan != nil {
		// Build the startup message with markdown link
		startupMessage := msg.Entry.Message
		if h.panelURL != "" {
			logsURL := fmt.Sprintf("%s/analytics/%s/apps/%s/logs",
				h.panelURL,
				msg.CollectorName,
				msg.AppName)
			startupMessage += fmt.Sprintf("\n\n[view logs](%s)", logsURL)
		}

		select {
		case h.notifyChan <- notify.Notification{
			Type:          notify.TypeStartup,
			CollectorName: msg.CollectorName,
			AppName:       msg.AppName,
			Message:       startupMessage,
		}:
		default:
			slog.Warn("Notification channel full, dropping startup notification")
		}
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
	if msg.Stats == nil || msg.CollectorName == "" {
		slog.Warn("Received stats without stats data or collector name")
		return
	}

	ts := msg.Stats.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	// Normalize to UTC for consistent storage
	ts = ts.UTC()

	err := h.ingestService.InsertSystemStats(ctx, ingest.CollectorName(msg.CollectorName), ingest.SystemStats{
		CollectorName:   msg.CollectorName,
		CpuUsagePercent: msg.Stats.CPUUsagePercent,
		MemTotalMb:      int64(msg.Stats.MemTotalMB),
		MemUsedMb:       int64(msg.Stats.MemUsedMB),
		MemAvailableMb:  int64(msg.Stats.MemAvailableMB),
		DiskTotalGb:     int64(msg.Stats.DiskTotalGB),
		DiskUsedGb:      int64(msg.Stats.DiskUsedGB),
		DiskAvailGb:     int64(msg.Stats.DiskAvailGB),
		CreatedAt:       ts,
	})
	if err != nil {
		slog.Error("Failed to store system stats", "error", err, "collector", msg.CollectorName)
		return
	}

	slog.Debug("Stored system stats", "collector", msg.CollectorName, "cpu", msg.Stats.CPUUsagePercent)
}
