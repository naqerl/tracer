package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/ai-shift/tracer/core/services/collectors"
	"github.com/ai-shift/tracer/core/services/dbmanager"
	"github.com/ai-shift/tracer/core/services/notify"
	"github.com/ai-shift/tracer/db"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Configure for production
	},
}

// LogMessage is the message format from collectors
type LogMessage struct {
	Type          string   `json:"type"` // "register", "log", or "startup"
	AppName       string   `json:"app_name"`
	Apps          []string `json:"apps,omitempty"`           // for registration
	CollectorName string   `json:"collector_name,omitempty"` // for registration
	Entry         LogEntry `json:"entry,omitempty"`
}

type LogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Level     string            `json:"level"`
	TraceID   string            `json:"trace_id"`
	Message   string            `json:"message"`
	Source    string            `json:"source"`
	Raw       string            `json:"raw"`
	Extra     map[string]string `json:"extra,omitempty"`
}

type handler struct {
	dbManager  *dbmanager.Manager
	sampleRate float64
	notifyChan notify.NotificationChan
	tracker    *collectors.Tracker
}

func InitMux(dbManager *dbmanager.Manager, sampleRate float64, notifyChan notify.NotificationChan, tracker *collectors.Tracker) *http.ServeMux {
	m := http.NewServeMux()
	h := &handler{
		dbManager:  dbManager,
		sampleRate: sampleRate,
		notifyChan: notifyChan,
		tracker:    tracker,
	}

	m.HandleFunc("GET /ws/ingest", h.handleWebSocket)
	m.HandleFunc("GET /collectors", h.listCollectors)

	return m
}

func (h *handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
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

func (h *handler) handleRegistration(ctx context.Context, connID, remoteAddr, collectorName string, apps []string) {
	slog.Info("Received app registration", "collector", collectorName, "remote", remoteAddr, "apps", apps, "count", len(apps))

	// Register collector with tracker
	h.tracker.Register(connID, collectorName, remoteAddr, apps)

	// Pre-create databases for registered apps
	for _, appName := range apps {
		if appName == "" {
			continue
		}
		if _, err := h.dbManager.GetQueries(appName); err != nil {
			slog.Error("Failed to register app", "app_name", appName, "error", err)
		} else {
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
	var msg LogMessage
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

	// Sampling logic: always persist errors, sample others
	isError := msg.Entry.Level == "error" || msg.Entry.Level == "ERROR"
	shouldPersist := isError || rand.Float64() < h.sampleRate

	if !shouldPersist {
		return
	}

	// Get queries for this app
	queries, err := h.dbManager.GetQueries(msg.AppName)
	if err != nil {
		slog.Error("Failed to get queries", "error", err, "app_name", msg.AppName)
		return
	}

	// Marshal extra context
	contextJSON := "{}"
	if len(msg.Entry.Extra) > 0 {
		if b, err := json.Marshal(msg.Entry.Extra); err == nil {
			contextJSON = string(b)
		}
	}

	// Store log entry
	ts := msg.Entry.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	_, err = queries.InsertLog(ctx, db.InsertLogParams{
		TraceID:   msg.Entry.TraceID,
		Level:     msg.Entry.Level,
		Message:   msg.Entry.Message,
		Source:    msg.Entry.Source,
		Context:   sql.NullString{String: contextJSON, Valid: true},
		Raw:       msg.Entry.Raw,
		CreatedAt: sql.NullTime{Time: ts, Valid: true},
	})
	if err != nil {
		slog.Error("Failed to store log", "error", err, "app_name", msg.AppName)
		return
	}

	// Send notification for errors
	if isError && h.notifyChan != nil {
		select {
		case h.notifyChan <- notify.Notification{
			Type:      notify.TypeAlert,
			AppName:   msg.AppName,
			TraceID:   msg.Entry.TraceID,
			Level:     msg.Entry.Level,
			Message:   msg.Entry.Message,
			Source:    msg.Entry.Source,
			Timestamp: ts,
		}:
		default:
			slog.Warn("Notification channel full, dropping notification")
		}
	}
}

// handleStartup processes app startup notifications
// Startup messages are always persisted and trigger a notification
func (h *handler) handleStartup(ctx context.Context, msg LogMessage) {
	if msg.AppName == "" {
		slog.Warn("Received startup without app_name")
		return
	}

	slog.Info("App startup detected", "app", msg.AppName, "message", msg.Entry.Message)

	// Get queries for this app
	queries, err := h.dbManager.GetQueries(msg.AppName)
	if err != nil {
		slog.Error("Failed to get queries for startup", "error", err, "app_name", msg.AppName)
		return
	}

	// Marshal extra context
	contextJSON := "{}"
	if len(msg.Entry.Extra) > 0 {
		if b, err := json.Marshal(msg.Entry.Extra); err == nil {
			contextJSON = string(b)
		}
	}

	// Store startup entry (always persist, no sampling)
	ts := msg.Entry.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	// Use "STARTUP" as the level for startup events
	level := "STARTUP"
	if msg.Entry.Level != "" {
		level = msg.Entry.Level
	}

	_, err = queries.InsertLog(ctx, db.InsertLogParams{
		TraceID:   msg.Entry.TraceID,
		Level:     level,
		Message:   msg.Entry.Message,
		Source:    msg.Entry.Source,
		Context:   sql.NullString{String: contextJSON, Valid: true},
		Raw:       msg.Entry.Raw,
		CreatedAt: sql.NullTime{Time: ts, Valid: true},
	})
	if err != nil {
		slog.Error("Failed to store startup log", "error", err, "app_name", msg.AppName)
		return
	}

	// Send startup notification
	if h.notifyChan != nil {
		select {
		case h.notifyChan <- notify.Notification{
			Type:      notify.TypeNotice,
			AppName:   msg.AppName,
			TraceID:   msg.Entry.TraceID,
			Level:     "STARTUP",
			Message:   msg.Entry.Message,
			Source:    msg.Entry.Source,
			Timestamp: ts,
		}:
		default:
			slog.Warn("Notification channel full, dropping startup notification")
		}
	}
}

func (h *handler) listCollectors(w http.ResponseWriter, r *http.Request) {
	collectors := h.tracker.List()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(collectors); err != nil {
		slog.Error("Failed to encode collectors", "error", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
	}
}
