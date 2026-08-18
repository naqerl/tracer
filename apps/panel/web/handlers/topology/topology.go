package topology

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"tracer/apps/panel/internal/domain"
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

// graphSnapshot is the web-layer view of the topology: it carries the element
// ids the frontend graph script keys its SVG nodes and edges on.
type graphSnapshot struct {
	Panel      graphPanel       `json:"panel"`
	Collectors []graphCollector `json:"collectors"`
}

type graphPanel struct {
	ID string `json:"id"`
}

type graphCollector struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	Apps []graphApp `json:"apps"`
}

type graphApp struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	HasHealth bool   `json:"has_health"`
}

// toGraphSnapshot maps a topology snapshot to the graph DTO, deriving the
// element ids from collector/app names so the service never shapes for the UI.
// The collector and app indices keep ids distinct when names sanitize to the
// same id.
func toGraphSnapshot(snapshot topologysvc.Snapshot) graphSnapshot {
	out := graphSnapshot{
		Panel:      graphPanel{ID: "panel-root"},
		Collectors: make([]graphCollector, 0, len(snapshot.Collectors)),
	}
	appSeq := 0
	for ci, c := range snapshot.Collectors {
		apps := make([]graphApp, 0, len(c.Apps))
		for _, a := range c.Apps {
			apps = append(apps, graphApp{
				ID:        fmt.Sprintf("app-%s-%s-%d", templates.SanitizeID(c.Name), templates.SanitizeID(a.Name), appSeq),
				Name:      a.Name,
				HasHealth: a.HasHealth,
			})
			appSeq++
		}
		out.Collectors = append(out.Collectors, graphCollector{
			ID:   fmt.Sprintf("collector-%s-%d", templates.SanitizeID(c.Name), ci),
			Name: c.Name,
			Apps: apps,
		})
	}
	return out
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
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}

	_, err = h.svc.GetSnapshot(r.Context(), user)
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
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}

	snapshot, err := h.svc.GetSnapshot(r.Context(), user)
	if err != nil {
		templates.ErrorPage("Error", "Failed to load topology", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return
	}

	snapshotJSON, err := json.Marshal(toGraphSnapshot(snapshot))
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
	user, ok := authmw.GetActor(r).(domain.User)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID := user.ID

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
