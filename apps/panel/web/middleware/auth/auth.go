package auth

import (
	"context"
	"errors"
	"net/http"

	authsvc "tracer/apps/panel/internal/services/auth"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/web/middleware"
)

type contextKey string

const (
	loginPathContextKey contextKey = "login_path"
	sessionCookie       string     = "tracer_session"
	defaultLoginPath    string     = "/auth/login"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
)

// GetUserID returns the authenticated user ID from the request context.
func GetUserID(r *http.Request) (int64, error) {
	userID, ok := middleware.GetUserID(r)
	if !ok {
		return userID, ErrUnauthorized
	}
	return userID, nil
}

// Require returns user if authenticated, otherwise writes 401.
func Require(w http.ResponseWriter, r *http.Request) (int64, error) {
	userID, err := GetUserID(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return userID, ErrUnauthorized
	}
	return userID, nil
}

// RequireOrRedirect returns user if authenticated, otherwise redirects to login.
func RequireOrRedirect(w http.ResponseWriter, r *http.Request) (int64, error) {
	userID, err := GetUserID(r)
	if err != nil {
		RedirectToLogin(w, r)
		return userID, ErrUnauthorized
	}
	return userID, nil
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
	loginPath := LoginPath(r)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", loginPath)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	http.Redirect(w, r, loginPath, http.StatusSeeOther)
}

// BuildAuthMiddleware enriches requests with auth context.
func BuildAuthMiddleware(authService *authsvc.Service, userService *usersvc.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if middleware.HasUserID(r) {
				next.ServeHTTP(w, r)
				return
			}
			if middleware.IsMarkdownFormat(r) {
				attachMarkdownContext(w, r, next, authService, userService)
				return
			}
			attachSessionContext(w, r, next, authService)
		})
	}
}

func attachMarkdownContext(w http.ResponseWriter, r *http.Request, next http.Handler, authService *authsvc.Service, userService *usersvc.Service) {
	apiKey := r.Header.Get("X-API-Key")
	if apiKey != "" {
		userID, err := authService.ValidateUserAPIKey(r.Context(), apiKey, userService)
		if err == nil {
			r = middleware.SetUserID(r, userID)
			next.ServeHTTP(w, r)
			return
		}
	}
	attachSessionContext(w, r, next, authService)
}

func attachSessionContext(w http.ResponseWriter, r *http.Request, next http.Handler, authService *authsvc.Service) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		next.ServeHTTP(w, r)
		return
	}

	userID, err := authService.ValidateSession(r.Context(), cookie.Value)
	if err != nil {
		http.SetCookie(w, &http.Cookie{
			Name:   sessionCookie,
			Value:  "",
			Path:   "/",
			MaxAge: -1,
		})
		next.ServeHTTP(w, r)
		return
	}

	r = middleware.SetUserID(r, userID)
	next.ServeHTTP(w, r)
}

func SetSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookie,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}
