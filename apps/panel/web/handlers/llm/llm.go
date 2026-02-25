package llm

import (
	"embed"
	"net/http"

	"tracer/apps/panel/web/middleware"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t templates.Templates
}

func InitMux() *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t: templates.New(views),
	}

	// LLM documentation
	m.HandleFunc("GET /llm", h.llmDocs)

	return m
}

// llmDocs serves documentation about available markdown endpoints for LLM tools
func (h *handler) llmDocs(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireOrRedirect(w, r)
	if err != nil {
		return
	}

	data := map[string]any{
		"Title":   "LLM API Documentation",
		"User":    user,
		"IsAdmin": user.IsAdmin,
		"BaseURL": r.Host,
	}

	// Render as markdown for /llm.md or regular HTML for /llm
	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "llm-docs-markdown", data)
	} else {
		h.t.RenderHTTP(w, r, "llm-docs", data)
	}
}
