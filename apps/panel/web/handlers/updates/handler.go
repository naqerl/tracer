// Package updates provides HTTP handlers for the binary update system.
// It exposes JSON API endpoints for managing update plans.
package updates

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	updatesvc "tracer/apps/panel/internal/services/update"
)

// Handler serves the update management HTTP API.
type Handler struct {
	svc *updatesvc.Service
}

// New creates an update Handler backed by the given service.
func New(svc *updatesvc.Service) *Handler {
	return &Handler{svc: svc}
}

// InitMux creates an HTTP mux with update-related routes.
func InitMux(svc *updatesvc.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := New(svc)

	m.HandleFunc("GET /api/plans", h.listPlans)
	m.HandleFunc("POST /api/plans", h.startPlan)
	m.HandleFunc("GET /api/plans/{id}", h.getPlan)
	m.HandleFunc("GET /api/plans/{id}/events", h.planEvents)
	m.HandleFunc("POST /api/plans/{id}/cancel", h.cancelPlan)

	return m
}

// ── Handlers ─────────────────────────────────────────────────────────────────

func (h *Handler) listPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.svc.ListPlans(r.Context())
	if err != nil {
		slog.Error("list plans failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, plans)
}

func (h *Handler) getPlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "plan id required", http.StatusBadRequest)
		return
	}

	plan, targets, err := h.svc.GetPlan(r.Context(), id)
	if err != nil {
		slog.Error("get plan failed", "error", err, "plan_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if plan == nil {
		http.Error(w, "plan not found", http.StatusNotFound)
		return
	}

	resp := struct {
		Plan    *updatesvc.Plan    `json:"plan"`
		Targets []updatesvc.Target `json:"targets"`
	}{
		Plan:    plan,
		Targets: targets,
	}
	writeJSON(w, http.StatusOK, resp)
}

type startPlanRequest struct {
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	Checksum    string `json:"checksum"`
}

func (h *Handler) startPlan(w http.ResponseWriter, r *http.Request) {
	var req startPlanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Version == "" || req.DownloadURL == "" {
		http.Error(w, "version and download_url are required", http.StatusBadRequest)
		return
	}

	plan, err := h.svc.StartPlan(r.Context(), req.Version, req.DownloadURL, req.Checksum)
	if err != nil {
		slog.Error("start plan failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, plan)
}

func (h *Handler) cancelPlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "plan id required", http.StatusBadRequest)
		return
	}

	if err := h.svc.CancelPlan(r.Context(), id); err != nil {
		if errors.Is(err, updatesvc.ErrPlanNotActive) {
			http.Error(w, "plan is not in a cancellable state", http.StatusConflict)
			return
		}
		slog.Error("cancel plan failed", "error", err, "plan_id", id)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"cancelled"}`))
}

// planEvents streams real-time progress updates via Server-Sent Events.
func (h *Handler) planEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "plan id required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send existing events first.
	for _, evt := range h.svc.Tracker().Events(id) {
		data, err := json.Marshal(evt)
		if err != nil {
			slog.Error("could not marshal plan event", "error", err)
			continue
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
	}
	flusher.Flush()

	// Subscribe for new events.
	unsub := h.svc.Tracker().Subscribe(func(evt updatesvc.PlanEvent) {
		if evt.PlanID != id {
			return
		}
		data, err := json.Marshal(evt)
		if err != nil {
			slog.Error("could not marshal plan event", "error", err)
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Type, data)
		flusher.Flush()
	})
	defer unsub()

	<-r.Context().Done()
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("could not encode JSON response", "error", err)
	}
}
