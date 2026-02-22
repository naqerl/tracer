package panel

import (
	"net/http"

	"github.com/ai-shift/tracer/apps/panel/web/middleware"
	"github.com/ai-shift/tracer/apps/panel/web/templates"
)

func (h *handler) dashboardPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAuthRedirect(w, r)
	if user == nil {
		return
	}

	collectorsWithStats, err := h.getFilteredCollectors(r, user)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":      "Panel",
		"User":       user,
		"IsAdmin":    user.IsAdmin == 1,
		"Nav":        templates.NewNavBuilder().Add("panel", "panel").Build(),
		"Collectors": collectorsWithStats,
	}

	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "dashboard-markdown", data)
		return
	}

	h.t.RenderHTTP(w, r, "dashboard", data)
}

func (h *handler) dashboardCollectors(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	collectorsWithStats, err := h.getFilteredCollectors(r, user)
	if err != nil {
		http.Error(w, "Failed to get collectors", http.StatusInternalServerError)
		return
	}

	h.t.RenderHTTP(w, r, "collectors-sections", map[string]any{"Collectors": collectorsWithStats})
}
