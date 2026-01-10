package llm

import (
	"embed"
	"net/http"

	"github.com/ai-shift/tracer/web/middleware"
	"github.com/ai-shift/tracer/web/templates"
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
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	nav := templates.NewNavBuilder().Add("llm", "LLM API").Build()

	data := map[string]any{
		"Title":   "LLM API Documentation",
		"User":    user,
		"IsAdmin": user.IsAdmin == 1,
		"Nav":     nav,
		"BaseURL": r.Host,
	}

	// Render as markdown for /llm.md or regular HTML for /llm
	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "llm-docs-markdown", data)
	} else {
		h.t.RenderHTTP(w, r, "llm-docs", data)
	}
}
