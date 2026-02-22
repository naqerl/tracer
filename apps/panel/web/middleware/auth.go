package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/ai-shift/tracer/apps/panel/internal/services/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/services/user"
	"github.com/ai-shift/tracer/apps/panel/db"
)

type contextKey string

const (
	userContextKey      contextKey = "user"
	loginPathContextKey contextKey = "login_path"
	sessionCookie       string     = "tracer_session"
	defaultLoginPath    string     = "/login"
)

// GetUser returns the authenticated user from the request context
func GetUser(r *http.Request) *db.User {
	user, ok := r.Context().Value(userContextKey).(*db.User)
	if !ok {
		return nil
	}
	return user
}

// RequireAuth returns the user if authenticated, otherwise writes 401 and returns nil
func RequireAuth(w http.ResponseWriter, r *http.Request) *db.User {
	user := GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil
	}
	return user
}

func RequireAuthRedirect(w http.ResponseWriter, r *http.Request) *db.User {
	user := GetUser(r)
	if user == nil {
		RedirectToLogin(w, r)
		return nil
	}
	return user
}

// RequireAdmin returns the user if authenticated and admin, otherwise writes error and returns nil
func RequireAdmin(w http.ResponseWriter, r *http.Request) *db.User {
	user := GetUser(r)
	if user == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil
	}
	if user.IsAdmin != 1 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return nil
	}
	return user
}

// IsAdmin checks if the current user is an admin
func IsAdmin(r *http.Request) bool {
	user := GetUser(r)
	return user != nil && user.IsAdmin == 1
}

func BuildLoginPathMiddleware(loginPath string) func(http.Handler) http.Handler {
	if loginPath == "" {
		loginPath = defaultLoginPath
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), loginPathContextKey, loginPath)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func LoginPath(r *http.Request) string {
	if path, ok := r.Context().Value(loginPathContextKey).(string); ok && path != "" {
		return path
	}
	return defaultLoginPath
}

func RedirectToLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, LoginPath(r), http.StatusSeeOther)
}

// BuildAuthMiddleware enriches requests with auth context.
// Handlers are responsible for enforcing access (redirect/401/403).
func BuildAuthMiddleware(authService *auth.Service, userService *user.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Ingest paths use API key authentication
			if strings.HasPrefix(path, "/ingest/") {
				handleAPIAuth(w, r, next, authService, userService)
				return
			}

			// Markdown format paths can use either session or API key auth
			if IsMarkdownFormat(r) {
				attachMarkdownContext(w, r, next, authService, userService)
				return
			}

			// All other paths attempt to attach session context; handlers decide on access control
			attachSessionContext(w, r, next, authService)
		})
	}
}

func handleAPIAuth(w http.ResponseWriter, r *http.Request, next http.Handler, authService *auth.Service, userService *user.Service) {
	// Get API key from header or query parameter
	apiKey := r.Header.Get("X-API-Key")
	if apiKey == "" {
		apiKey = r.URL.Query().Get("api_key")
	}

	if apiKey == "" {
		http.Error(w, "API key required", http.StatusUnauthorized)
		return
	}

	// First try validating as user API key
	user, err := authService.ValidateUserAPIKey(r.Context(), apiKey, userService)
	if err == nil {
		// Valid user API key - add user to context
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
		return
	}

	// Fall back to global collector API key
	valid, err := authService.ValidateAPIKey(r.Context(), apiKey)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if !valid {
		http.Error(w, "Invalid API key", http.StatusUnauthorized)
		return
	}

	next.ServeHTTP(w, r)
}

func attachMarkdownContext(w http.ResponseWriter, r *http.Request, next http.Handler, authService *auth.Service, userService *user.Service) {
	// Try API key first (from header or query)
	apiKey := r.Header.Get("X-API-Key")
	if apiKey == "" {
		apiKey = r.URL.Query().Get("api_key")
	}

	if apiKey != "" {
		user, err := authService.ValidateUserAPIKey(r.Context(), apiKey, userService)
		if err == nil {
			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
	}

	// Fall back to session auth for context attachment
	attachSessionContext(w, r, next, authService)
}

func attachSessionContext(w http.ResponseWriter, r *http.Request, next http.Handler, authService *auth.Service) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		next.ServeHTTP(w, r)
		return
	}

	user, err := authService.ValidateSession(r.Context(), cookie.Value)
	if err != nil {
		// Clear invalid cookie
		http.SetCookie(w, &http.Cookie{
			Name:   sessionCookie,
			Value:  "",
			Path:   "/",
			MaxAge: -1,
		})
		next.ServeHTTP(w, r)
		return
	}

	// Add user to context
	ctx := context.WithValue(r.Context(), userContextKey, user)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// SetSessionCookie sets the session cookie
func SetSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie clears the session cookie
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookie,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}
