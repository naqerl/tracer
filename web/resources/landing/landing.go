package landing

import (
	"embed"
	"net/http"

	"github.com/ai-shift/tracer/core/services/auth"
	"github.com/ai-shift/tracer/web/middleware"
	"github.com/ai-shift/tracer/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t           templates.Templates
	authService *auth.Service
}

func InitHandler(authService *auth.Service) http.HandlerFunc {
	h := handler{
		t:           templates.New(views),
		authService: authService,
	}
	return h.landingPage
}

func (h handler) landingPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	isAuthenticated := user != nil

	username := ""
	if isAuthenticated {
		username = user.Username
	}

	data := map[string]any{
		"Title":           "Tracer - Lightweight Log Aggregation",
		"IsAuthenticated": isAuthenticated,
		"Username":        username,
	}

	h.t.RenderHTTP(w, r, "landing", data)
}
