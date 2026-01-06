package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"math/rand"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ai-shift/tracer/core/services/alerter"
	"github.com/ai-shift/tracer/core/services/dbmanager"
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
	Type    string   `json:"type"` // "register" or "log"
	AppName string   `json:"app_name"`
	Apps    []string `json:"apps,omitempty"` // for registration
	Entry   LogEntry `json:"entry,omitempty"`
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

// Alert is sent to the alerter service (re-exported from alerter package)
type Alert = alerter.Alert

type handler struct {
	dbManager  *dbmanager.Manager
	sampleRate float64
	alertChan  chan<- Alert
}

func InitMux(dbManager *dbmanager.Manager, sampleRate float64, alertChan chan<- Alert) *http.ServeMux {
	m := http.NewServeMux()
	h := &handler{
		dbManager:  dbManager,
		sampleRate: sampleRate,
		alertChan:  alertChan,
	}

	m.HandleFunc("GET /ws/ingest", h.handleWebSocket)

	return m
}

func (h *handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	slog.Info("WebSocket connected", "remote", r.RemoteAddr)

	// Configure connection with generous timeout
	conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(5 * time.Minute))
		return nil
	})

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

		h.processMessage(r.Context(), message)
	}
}

func (h *handler) handleRegistration(ctx context.Context, apps []string) {
	slog.Info("Received app registration", "apps", apps, "count", len(apps))

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

func (h *handler) processMessage(ctx context.Context, message []byte) {
	var msg LogMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		slog.Error("Failed to parse log message", "error", err)
		return
	}

	// Handle registration messages
	if msg.Type == "register" {
		h.handleRegistration(ctx, msg.Apps)
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

	// Send alert for errors
	if isError && h.alertChan != nil {
		select {
		case h.alertChan <- Alert{
			AppName:   msg.AppName,
			TraceID:   msg.Entry.TraceID,
			Level:     msg.Entry.Level,
			Message:   msg.Entry.Message,
			Source:    msg.Entry.Source,
			Timestamp: ts,
		}:
		default:
			slog.Warn("Alert channel full, dropping alert")
		}
	}
}
