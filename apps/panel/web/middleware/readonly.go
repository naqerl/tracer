package middleware

import "net/http"

// BuildReadOnlyMiddleware rejects every state-changing method. Read-only mode
// is a global on/off switch (see the panel's -readonly flag); the e2e stack
// toggles it via E2E_PANEL_READONLY instead of carving out exceptions.
func BuildReadOnlyMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
