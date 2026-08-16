package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"tracer/apps/panel/internal/domain"
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
)

// GetActor returns the caller actor from the request context, or
// domain.Unauthenticated when no session or API key is present.
func GetActor(r *http.Request) domain.Actor {
	if actor, ok := middleware.GetActor(r); ok {
		return actor
	}
	return domain.Unauthenticated{}
}

// RequireUser returns the authenticated caller, redirecting to login when
// the request is not authenticated. Role checks are the caller's job.
func RequireUser(w http.ResponseWriter, r *http.Request) (domain.User, error) {
	actor := GetActor(r)
	user, ok := actor.(domain.User)
	if !ok {
		RedirectToLogin(w, r)
		return domain.User{}, ErrUnauthorized
	}
	return user, nil
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
			attachSessionContext(w, r, next, authService, userService)
		})
	}
}

func attachMarkdownContext(w http.ResponseWriter, r *http.Request, next http.Handler, authService *authsvc.Service, userService *usersvc.Service) {
	apiKey := r.Header.Get("X-API-Key")
	if apiKey != "" {
		userID, err := authService.ValidateUserAPIKey(r.Context(), apiKey)
		if err == nil {
			r = setActorForUser(r, userService, userID)
			next.ServeHTTP(w, r)
			return
		}
	}
	attachSessionContext(w, r, next, authService, userService)
}

func attachSessionContext(w http.ResponseWriter, r *http.Request, next http.Handler, authService *authsvc.Service, userService *usersvc.Service) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		next.ServeHTTP(w, r)
		return
	}

	userID, refresh, err := authService.ValidateSession(r.Context(), cookie.Value)
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

	// A half-expired session was extended server-side; re-issue the cookie so
	// its browser-side expiry matches the new deadline.
	if refresh {
		SetSessionCookie(w, r, cookie.Value, int(authsvc.SessionDuration.Seconds()))
	}

	r = setActorForUser(r, userService, userID)
	next.ServeHTTP(w, r)
}

// setActorForUser resolves the user and stores the full domain.Actor (with
// role and display fields) in the request context. Unknown users fall back to
// an unauthenticated actor rather than granting access.
func setActorForUser(r *http.Request, userService *usersvc.Service, userID int64) *http.Request {
	user, err := userService.FindByID(r.Context(), domain.System{}, userID)
	if err != nil {
		slog.Error("could not resolve actor from session user", "user_id", userID, "error", err)
		return middleware.SetActor(r, domain.Unauthenticated{})
	}
	return middleware.SetActor(r, user)
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
