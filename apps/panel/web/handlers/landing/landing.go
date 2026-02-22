package landing

import (
	"embed"
	"net/http"

	"github.com/ai-shift/tracer/apps/panel/web/middleware"
	authmw "github.com/ai-shift/tracer/apps/panel/web/middleware/auth"
	"github.com/ai-shift/tracer/apps/panel/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t templates.Templates
}

func InitHandler() http.HandlerFunc {
	h := handler{
		t: templates.New(views),
	}
	return h.landingPage
}

func (h handler) landingPage(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.GetUser(r)
	isAuthenticated := err == nil

	username := ""
	if isAuthenticated {
		username = user.Username
	}

	data := map[string]any{
		"Title":           "Tracer - Lightweight Log Aggregation",
		"IsAuthenticated": isAuthenticated,
		"Username":        username,
	}

	if middleware.IsMarkdownFormat(r) {
		h.t.RenderMarkdown(w, r, "landing-markdown", data)
	} else {
		h.t.RenderHTTP(w, r, "landing", data)
	}
}
