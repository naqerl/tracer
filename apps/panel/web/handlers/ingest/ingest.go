package ingest

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	panelSvc "tracer/apps/panel/internal/services/panel"
	topologySvc "tracer/apps/panel/internal/services/topology"
	"tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/transport"
	"tracer/apps/panel/web/middleware"
	"tracer/libs/types"
	"tracer/libs/wsx"
)

type handler struct {
	authService   *auth.Service
	ingestService *ingest.Service
	tracker       *ingest.Tracker
	topology      *topologySvc.Service
	panelService  *panelSvc.Service
	userService   *user.Service
	conns         *transport.Connections
}

func InitMux(authService *auth.Service, ingestService *ingest.Service, tracker *ingest.Tracker, topology *topologySvc.Service, panelService *panelSvc.Service, userService *user.Service, conns *transport.Connections) *http.ServeMux {
	m := http.NewServeMux()
	h := &handler{
		authService:   authService,
		ingestService: ingestService,
		tracker:       tracker,
		topology:      topology,
		panelService:  panelService,
		userService:   userService,
		conns:         conns,
	}

	m.HandleFunc("GET /ws", h.handleWebSocket)
	m.HandleFunc("PUT /config/{collector}", h.putCollectorConfig)
	m.HandleFunc("POST /format/{collector}", h.guessFormat)

	return m
}

// Handlers

func (h *handler) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !h.requireCollectorAPIKey(w, r) {
		return
	}

	connID := uuid.New().String()
	remoteAddr := extractIP(r.RemoteAddr)

	// Collector name once the connection registers, so we can drop it from
	// the live-connection registry on disconnect.
	var collectorName string

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
		if collectorName != "" {
			h.conns.Remove(collectorName, client)
		}
		slog.Info("WebSocket disconnected", "remote", remoteAddr, "conn_id", connID)
	}()

	// Process events from Recv until the channel is closed.
	for evt := range client.Recv() {
		switch e := evt.(type) {
		case types.RegisterEvent:
			h.topology.Publish(topologySvc.Message{
				Register: &e,
			})
			collectorName = e.Collector
			h.conns.Add(collectorName, client)
			if h.handleRegistration(r.Context(), connID, remoteAddr, e) {
				go h.sendCollectorConfig(r.Context(), client, collectorName)
			}
		case types.FormatResultEvent:
			h.conns.HandleFormatResult(e)
		case types.Envelope:
			h.handleEnvelope(r.Context(), e)
		default:
			slog.Warn("Unknown event type from Recv", "event_type", evt.EventType())
		}
	}
}

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
		h.topology.Publish(topologySvc.Message{
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
		h.topology.Publish(topologySvc.Message{
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

// handleRegistration records the collector on this connection. Config is sent
// separately after this returns.
func (h *handler) handleRegistration(ctx context.Context, connID, remoteAddr string, msg types.RegisterEvent) bool {
	if err := domain.ValidateCollectorName(msg.Collector); err != nil {
		slog.Error("Invalid registration request", "error", err, "remote", remoteAddr, "conn_id", connID)
		return false
	}

	_, alreadyConnected := h.tracker.Get(msg.Collector)
	h.tracker.Register(connID, msg.Collector, remoteAddr, msg.Apps)

	if len(msg.Apps) == 0 {
		slog.Info("Empty collector registration", "collector", msg.Collector, "remote", remoteAddr, "conn_id", connID)
		return true
	}

	req := ingest.RegisterCollectorRequest{
		CollectorName: msg.Collector,
		Apps:          msg.Apps,
	}

	if err := req.Validate(); err != nil {
		slog.Error("Invalid registration request", "error", err, "remote", remoteAddr, "conn_id", connID)
		return false
	}

	slog.Info("Received app registration", "ingest", req.CollectorName, "remote", remoteAddr, "apps", req.Apps, "count", len(req.Apps))

	if err := h.ingestService.RegisterCollector(ctx, req); err != nil {
		slog.Error("Failed to register collector", "collector", req.CollectorName, "error", err)
		return false
	}

	for _, appName := range req.Apps {
		if appName != "" {
			slog.Info("Registered app", "app_name", appName)
		}
	}

	return !alreadyConnected
}

// sendCollectorConfig guesses tracer-collector / tracer-panel when those apps
// are missing, persists the result, then pushes stored config.
func (h *handler) sendCollectorConfig(ctx context.Context, client *wsx.Client, collectorName string) {
	guessCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	if err := h.panelService.EnsureSelfApps(guessCtx, collectorName); err != nil {
		slog.Error("could not ensure self apps", "collector", collectorName, "error", err)
	}
	cancel()
	h.pushStoredConfig(ctx, client, collectorName)
}

// pushStoredConfig loads the panel-stored config for a collector and sends it
// over the WebSocket connection.
func (h *handler) pushStoredConfig(ctx context.Context, client *wsx.Client, collectorName string) {
	cfgJSON, err := h.panelService.FetchCollectorConfig(ctx, collectorName)
	if err != nil {
		slog.Error("could not load collector config to push", "collector", collectorName, "error", err)
		return
	}
	var cfg types.CollectorConfig
	if cfgJSON != "" {
		if err := json.Unmarshal([]byte(cfgJSON), &cfg); err != nil {
			slog.Error("could not parse stored config", "collector", collectorName, "error", err)
			return
		}
	}
	if cfg.Stats == nil {
		def := types.DefaultStatsConfig()
		cfg.Stats = &def
	}

	out, err := json.Marshal(&cfg)
	if err != nil {
		slog.Error("could not marshal config", "collector", collectorName, "error", err)
		return
	}
	if err := client.Send(types.ConfigEvent{Type: types.EventTypeConfig, Config: string(out)}); err != nil {
		slog.Error("failed to push config to collector", "collector", collectorName, "error", err)
		return
	}
	slog.Info("pushed config to collector", "collector", collectorName)
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
		msg := topologySvc.Message{
			Persisted: &topologySvc.PersistedLogMessage{
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

	userID, err := h.authService.ValidateCollectorAPIKey(r.Context(), apiKey)
	if err != nil {
		http.Error(w, "Invalid API key", http.StatusUnauthorized)
		return false
	}

	// Resolve the user from the collector API key's user_id.
	user, err := h.userService.FindByID(r.Context(), domain.System{}, userID)
	if err != nil {
		slog.Error("could not resolve user from collector API key", "user_id", userID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return false
	}

	*r = *middleware.SetActor(r, user)
	return true
}

// putCollectorConfig stores and pushes the configuration for a
// collector, authenticated with the collector API key. It is used to seed
// and update collector config from the e2e/demo workflow; the collector
// itself pushes no config, so production uses the session-authenticated
// settings endpoint instead.
func (h *handler) putCollectorConfig(w http.ResponseWriter, r *http.Request) {
	if !h.requireCollectorAPIKey(w, r) {
		return
	}

	collector := r.PathValue("collector")
	if err := domain.ValidateCollectorName(collector); err != nil {
		http.Error(w, "Invalid collector name", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB
	if err != nil {
		http.Error(w, "Could not read body", http.StatusBadRequest)
		return
	}
	if !json.Valid(body) || strings.TrimSpace(string(body)) == "" {
		http.Error(w, "Config JSON body is required", http.StatusBadRequest)
		return
	}

	if err := h.panelService.SaveCollectorConfig(r.Context(), collector, string(body)); err != nil {
		slog.Error("Failed to save collector config", "error", err, "collector", collector)
		http.Error(w, "Could not save collector config", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// guessFormat sends a format guess request to a collector via WebSocket
// and waits for the result. The user provides only a service name.
func (h *handler) guessFormat(w http.ResponseWriter, r *http.Request) {
	if !h.requireCollectorAPIKey(w, r) {
		return
	}

	collector := r.PathValue("collector")
	if err := domain.ValidateCollectorName(collector); err != nil {
		http.Error(w, "Invalid collector name", http.StatusBadRequest)
		return
	}

	var req struct {
		Service string `json:"service"`
		AppName string `json:"app_name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64*1024)).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	service := strings.TrimSpace(req.Service)
	if service == "" {
		http.Error(w, "service is required", http.StatusBadRequest)
		return
	}
	appName := strings.TrimSpace(req.AppName)
	if appName == "" {
		appName = service
	}

	requestID := uuid.New().String()
	ch := h.conns.RegisterFormatResult(requestID)

	err := h.conns.SendFormatRequest(collector, types.FormatRequestEvent{
		Type:      types.EventTypeFormatRequest,
		RequestID: requestID,
		App:       appName,
		Service:   service,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	result, err := h.conns.WaitForFormatResult(r.Context(), ch)
	if err != nil {
		http.Error(w, "format guess timed out", http.StatusGatewayTimeout)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(result); err != nil {
		slog.Error("could not encode format result", "error", err)
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
