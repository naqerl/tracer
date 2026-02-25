package auth

import (
	"context"
	"errors"
	"net/http"

	"tracer/apps/panel/db"
	authsvc "tracer/apps/panel/internal/services/auth"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/web/middleware"
	sharedtypes "tracer/libs/types"
)

type contextKey string

const (
	userContextKey      contextKey = "user"
	loginPathContextKey contextKey = "login_path"
	sessionCookie       string     = "tracer_session"
	defaultLoginPath    string     = "/login"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
)

// GetUser returns the authenticated user from the request context.
func GetUser(r *http.Request) (sharedtypes.User, error) {
	user, ok := r.Context().Value(userContextKey).(sharedtypes.User)
	if !ok {
		return sharedtypes.User{}, ErrUnauthorized
	}
	return user, nil
}

// Require returns user if authenticated, otherwise writes 401.
func Require(w http.ResponseWriter, r *http.Request) (sharedtypes.User, error) {
	user, err := GetUser(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return sharedtypes.User{}, ErrUnauthorized
	}
	return user, nil
}

// RequireOrRedirect returns user if authenticated, otherwise redirects to login.
func RequireOrRedirect(w http.ResponseWriter, r *http.Request) (sharedtypes.User, error) {
	user, err := GetUser(r)
	if err != nil {
		RedirectToLogin(w, r)
		return sharedtypes.User{}, ErrUnauthorized
	}
	return user, nil
}

// RequireAdmin returns user if admin, otherwise writes 401/403.
func RequireAdmin(w http.ResponseWriter, r *http.Request) (sharedtypes.User, error) {
	user, err := GetUser(r)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return sharedtypes.User{}, ErrUnauthorized
	}
	if !user.IsAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return sharedtypes.User{}, ErrForbidden
	}
	return user, nil
}

func IsAdmin(r *http.Request) bool {
	user, err := GetUser(r)
	return err == nil && user.IsAdmin
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
func BuildAuthMiddleware(authService *authsvc.Service, userService *usersvc.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		user, err := authService.ValidateUserAPIKey(r.Context(), apiKey, userService)
		if err == nil {
			ctx := context.WithValue(r.Context(), userContextKey, toPrincipalUser(user))
			next.ServeHTTP(w, r.WithContext(ctx))
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

	user, err := authService.ValidateSession(r.Context(), cookie.Value)
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

	ctx := context.WithValue(r.Context(), userContextKey, toPrincipalUser(user))
	next.ServeHTTP(w, r.WithContext(ctx))
}

func toPrincipalUser(user *db.User) sharedtypes.User {
	if user == nil {
		return sharedtypes.User{}
	}
	return sharedtypes.User{
		ID:       user.ID,
		Username: user.Username,
		IsAdmin:  user.IsAdmin == 1,
	}
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
