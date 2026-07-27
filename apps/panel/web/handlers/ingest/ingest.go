package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	topologysvc "tracer/apps/panel/internal/services/topology"
	"tracer/libs/types"
	"tracer/libs/wsx"
)

// ClientStore maps collector names to their WebSocket clients, allowing the
// update service to send commands to specific collectors.
type ClientStore struct {
	mu      sync.RWMutex
	clients map[string]*wsx.Client
}

// NewClientStore creates an empty ClientStore.
func NewClientStore() *ClientStore {
	return &ClientStore{
		clients: make(map[string]*wsx.Client),
	}
}

// Store associates a collector name with its WebSocket client.
func (s *ClientStore) Store(name string, client *wsx.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[name] = client
}

// Delete removes a collector name from the store.
func (s *ClientStore) Delete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, name)
}

// SendTo sends an event to the named collector. Returns an error if the
// collector is not connected.
func (s *ClientStore) SendTo(ctx context.Context, name string, msg types.Event) error {
	s.mu.RLock()
	client, ok := s.clients[name]
	s.mu.RUnlock()
	if !ok {
		return fmt.Errorf("collector %s is not connected", name)
	}
	return client.Send(msg)
}

// ConnectedNames returns the names of all currently connected collectors.
func (s *ClientStore) ConnectedNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.clients))
	for name := range s.clients {
		names = append(names, name)
	}
	return names
}

// DeleteByClient removes all entries that reference the given client pointer.
// Used on WebSocket disconnect to clean up stale references.
func (s *ClientStore) DeleteByClient(client *wsx.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, c := range s.clients {
		if c == client {
			delete(s.clients, name)
		}
	}
}

type handler struct {
	authService    *auth.Service
	ingestService  *ingest.Service
	tracker        *ingest.Tracker
	topology       *topologysvc.Service
	clients        *ClientStore
	onHello        func(ctx context.Context, hello types.HelloReport)
	onUpdateStatus func(ctx context.Context, report types.UpdateStatusReport)
}

func InitMux(authService *auth.Service, ingestService *ingest.Service, tracker *ingest.Tracker, topology *topologysvc.Service) *http.ServeMux {
	return InitMuxWithClientStore(authService, ingestService, tracker, topology, NewClientStore())
}

// InitMuxWithClientStore creates the mux with a pre-allocated ClientStore.
// This allows the caller to share the ClientStore reference with other
// components (e.g., the update service).
func InitMuxWithClientStore(authService *auth.Service, ingestService *ingest.Service, tracker *ingest.Tracker, topology *topologysvc.Service, clients *ClientStore) *http.ServeMux {
	return InitMuxWithCallbacks(authService, ingestService, tracker, topology, clients, nil, nil)
}

// InitMuxWithCallbacks creates the mux with optional callbacks for update
// events. This allows the update service to receive hello and update status
// reports without direct coupling.
func InitMuxWithCallbacks(authService *auth.Service, ingestService *ingest.Service, tracker *ingest.Tracker, topology *topologysvc.Service, clients *ClientStore, onHello func(ctx context.Context, hello types.HelloReport), onUpdateStatus func(ctx context.Context, report types.UpdateStatusReport)) *http.ServeMux {
	m := http.NewServeMux()
	h := &handler{
		authService:    authService,
		ingestService:  ingestService,
		tracker:        tracker,
		topology:       topology,
		clients:        clients,
		onHello:        onHello,
		onUpdateStatus: onUpdateStatus,
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
		// Clean up client store on disconnect.
		h.cleanupClientStore(client)
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
			h.handleRegistration(r.Context(), connID, remoteAddr, e, client)
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
	case types.EventTypeUpdateStatus:
		var evt types.UpdateStatusReport
		if err := env.DecodeData(&evt); err != nil {
			slog.Warn("Failed to decode update status event", "error", err, "collector", env.Collector)
			return
		}
		h.handleUpdateStatus(ctx, evt)
	case types.EventTypeHello:
		var evt types.HelloReport
		if err := env.DecodeData(&evt); err != nil {
			slog.Warn("Failed to decode hello event", "error", err, "collector", env.Collector)
			return
		}
		h.handleHello(ctx, evt)
	default:
		slog.Error("Unhandled envelope type", "type", env.Type)
	}
}

func (h *handler) handleRegistration(ctx context.Context, connID, remoteAddr string, msg types.RegisterEvent, client *wsx.Client) {
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

	// Store client reference for sending commands (e.g., updates).
	h.clients.Store(req.CollectorName, client)

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

// handleUpdateStatus processes an update status report from a collector.
// It delegates to the update service callback if set.
func (h *handler) handleUpdateStatus(ctx context.Context, report types.UpdateStatusReport) {
	slog.Info("update status report", "collector", report.Collector, "status", report.Status,
		"step", report.Step, "error", report.Error)
	if h.onUpdateStatus != nil {
		h.onUpdateStatus(ctx, report)
	}
}

// handleHello processes a hello report from a reconnecting collector.
// It delegates to the update service callback if set (for tracking activation).
func (h *handler) handleHello(ctx context.Context, hello types.HelloReport) {
	slog.Info("hello from collector", "collector", hello.Collector,
		"version", hello.Version, "has_pending_update", hello.HasPendingUpdate)
	if h.onHello != nil {
		h.onHello(ctx, hello)
	}
}

// cleanupClientStore removes all entries in the client store that reference
// the given client (by pointer comparison). This is called on disconnect to
// clean up stale entries.
func (h *handler) cleanupClientStore(client *wsx.Client) {
	h.clients.DeleteByClient(client)
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
