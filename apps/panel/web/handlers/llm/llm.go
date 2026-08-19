package llm

import "net/http"

func InitMux() *http.ServeMux {
	m := http.NewServeMux()

	// LLM documentation
	m.HandleFunc("GET /llm", RenderDocs)

	return m
}

// RenderDocs renders the LLM documentation as an HTML page. It is served at
// /llm and reused by the panel root so that pointing an agent at the root
// yields a starting prompt. The Accept middleware converts the page to
// markdown on demand.
func RenderDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	llmDocsHTML(r.Host).Render(r.Context(), w)
}
