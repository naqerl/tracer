package templates

import "net/http"

const (
	sinceCookieName   = "tracer_panel_since"
	defaultSinceLabel = "24h"
)

// RenderError writes the HTTP status and renders an error page with the
// correct since value from the user's cookie.
func RenderError(w http.ResponseWriter, r *http.Request, title, message string, status int) {
	sinceLabel := defaultSinceLabel
	if cookie, err := r.Cookie(sinceCookieName); err == nil {
		sinceLabel = cookie.Value
	}
	w.WriteHeader(status)
	ErrorPage(title, message, status, NavData{Since: sinceLabel}).
		Render(r.Context(), w)
}
