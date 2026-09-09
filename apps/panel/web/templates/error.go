package templates

import "net/http"

// RenderError writes the HTTP status and renders an error page with the
// correct since value from the user's cookie.
func RenderError(w http.ResponseWriter, r *http.Request, title, message string, status int) {
	sinceLabel := DefaultSinceLabel
	if cookie, err := r.Cookie(SinceCookieName); err == nil {
		sinceLabel = cookie.Value
	}
	w.WriteHeader(status)
	ErrorPage(title, message, status, NavData{Since: sinceLabel}).
		Render(r.Context(), w)
}
