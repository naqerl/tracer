package topology

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	topologysvc "tracer/apps/panel/internal/services/topology"
	usersvc "tracer/apps/panel/internal/services/user"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/templates"
	"tracer/libs/types"

	"tracer/apps/panel/web/handlers/topology/view"
)

type handler struct {
	svc     *topologysvc.Service
	userSvc *usersvc.Service
}

func InitMux(svc *topologysvc.Service, userSvc *usersvc.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		svc:     svc,
		userSvc: userSvc,
	}

	m.HandleFunc("GET /", h.getTopologyPage)
	m.HandleFunc("GET /graph", h.getTopologyGraph)
	m.HandleFunc("GET /events", h.streamTopologyEvents)

	return m
}

func (h *handler) getTopologyPage(w http.ResponseWriter, r *http.Request) {
	userID, err := authmw.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	_, err = h.svc.GetSnapshot(r.Context(), userID)
	if err != nil {
		templates.ErrorPage("Error", "Failed to load topology", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return
	}

	nav := templates.NavData{Since: "24h"} // TODO: read from cookie
	view.TopologyPage(view.TopologyPageProps{
		Title: "Topology - Tracer",
		Nav:   nav,
	}).Render(r.Context(), w)
}

func (h *handler) getTopologyGraph(w http.ResponseWriter, r *http.Request) {
	userID, err := authmw.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	snapshot, err := h.svc.GetSnapshot(r.Context(), userID)
	if err != nil {
		templates.ErrorPage("Error", "Failed to load topology", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return
	}

	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		templates.ErrorPage("Error", "Failed to serialize topology", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return
	}

	view.TopologyGraph(view.TopologyGraphProps{
		Title:    "Topology - Tracer",
		Snapshot: string(snapshotJSON),
	}).Render(r.Context(), w)
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

	if err := writeSSE(w, "tick", map[string]string{
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
			if err := writeSSE(w, "tick", map[string]string{
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
		if err := evt.DecodeData(&payload); err != nil {
			slog.Warn("Failed to decode log event for policy check", "error", err, "collector", evt.Collector)
			return false
		}
		return policy.AllowedTo(evt.Collector, payload.App)
	case types.EventTypeHealth:
		var payload types.HealthEvent
		if err := evt.DecodeData(&payload); err != nil {
			slog.Warn("Failed to decode health event for policy check", "error", err, "collector", evt.Collector)
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
		return "persisted", evt.Persisted, true
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
