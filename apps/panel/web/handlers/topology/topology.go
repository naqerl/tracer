package topology

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	topologysvc "tracer/apps/panel/internal/services/topology"
	usersvc "tracer/apps/panel/internal/services/user"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/templates"
	"tracer/libs/types"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t       templates.Templates
	svc     *topologysvc.Service
	userSvc *usersvc.Service
}

func InitMux(svc *topologysvc.Service, userSvc *usersvc.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:       templates.New(views),
		svc:     svc,
		userSvc: userSvc,
	}

	m.HandleFunc("GET /", h.getTopologyPage)
	m.HandleFunc("GET /events", h.streamTopologyEvents)

	return m
}

func (h *handler) getTopologyPage(w http.ResponseWriter, r *http.Request) {
	userID, err := authmw.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	snapshot, err := h.svc.GetSnapshot(r.Context(), userID)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to load topology", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "topology", map[string]any{
		"Title":    "Topology",
		"Snapshot": snapshot,
	})
}

func (h *handler) streamTopologyEvents(w http.ResponseWriter, r *http.Request) {
	userID, err := authmw.Require(w, r)
	if err != nil {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	policy, err := h.userSvc.FindAccessPolicy(r.Context(), userID)
	if err != nil {
		http.Error(w, "Failed to load access policy", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	if err := writeSSE(w, "panel_tick", map[string]string{
		"timestamp": time.Now().Format(time.RFC3339Nano),
	}); err != nil {
		return
	}
	flusher.Flush()

	events, stop := h.svc.Subscribe()
	defer stop()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := writeSSE(w, "panel_tick", map[string]string{
				"timestamp": time.Now().Format(time.RFC3339Nano),
			}); err != nil {
				return
			}
			flusher.Flush()
		case evt, ok := <-events:
			if !ok {
				return
			}
			if !allowedForPolicy(policy, evt) {
				continue
			}
			eventName, payload, ok := sseEvent(evt)
			if !ok {
				continue
			}
			if err := writeSSE(w, eventName, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func allowedForPolicy(policy usersvc.AccessPolicy, evt topologysvc.Message) bool {
	switch {
	case evt.Register != nil:
		return true
	case evt.Envelope != nil:
		return allowedEnvelopeForPolicy(policy, *evt.Envelope)
	case evt.Persisted != nil:
		return policy.AllowedTo(evt.Persisted.Collector, evt.Persisted.App)
	default:
		return true
	}
}

func allowedEnvelopeForPolicy(policy usersvc.AccessPolicy, evt types.Envelope) bool {
	if evt.Collector == "" {
		return true
	}

	switch evt.Type {
	case types.EventTypeStats:
		return policy.AllowedTo(evt.Collector, "")
	case types.EventTypeLog:
		var payload types.LogEvent
		if err := json.Unmarshal(evt.Data, &payload); err != nil {
			return false
		}
		return policy.AllowedTo(evt.Collector, payload.App)
	case types.EventTypeHealth:
		var payload types.HealthEvent
		if err := json.Unmarshal(evt.Data, &payload); err != nil {
			return false
		}
		return policy.AllowedTo(evt.Collector, payload.App)
	default:
		return true
	}
}

func sseEvent(evt topologysvc.Message) (string, any, bool) {
	switch {
	case evt.Register != nil:
		return string(types.EventTypeRegister), evt.Register, true
	case evt.Envelope != nil:
		return string(evt.Envelope.Type), evt.Envelope, true
	case evt.Persisted != nil:
		return "log_persisted", evt.Persisted, true
	default:
		return "", nil, false
	}
}

func writeSSE(w http.ResponseWriter, event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal sse payload: %w", err)
	}

	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	return nil
}
