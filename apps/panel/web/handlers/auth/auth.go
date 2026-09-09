package auth

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/user"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/middleware/ratelimit"
	"tracer/apps/panel/web/templates"
)

type handler struct {
	authService *auth.Service
	userService *user.Service
	sessionMax  time.Duration
}

func InitMux(authService *auth.Service, userService *user.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		authService: authService,
		userService: userService,
		sessionMax:  auth.SessionDuration,
	}

	// Login routes
	m.HandleFunc("GET /login", h.loginPage)
	m.Handle("POST /login", rateLimitMiddleware(ratelimit.LimitOf("login"))(http.HandlerFunc(h.login)))
	m.HandleFunc("POST /logout", h.logout)

	// Setup routes
	m.HandleFunc("GET /setup", h.setupPage)
	m.Handle("POST /setup", rateLimitMiddleware(ratelimit.LimitOf("setup"))(http.HandlerFunc(h.setup)))

	return m
}

// rateLimitMiddleware creates a rate limiter: 10 requests per minute per IP.
var rateLimitMiddleware = ratelimit.NewTokenBucket(10, 20, time.Minute).Middleware

// renderFormError renders an error message for HTMX form submission
func (h *handler) renderFormError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	fmt.Fprint(w, msg)
}

func (h *handler) renderSetupPage(w http.ResponseWriter, r *http.Request, errorMessage string) {
	component := SetupPage("Setup - Tracer", errorMessage)
	component.Render(r.Context(), w)
}

// Login handlers

func (h *handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := authmw.GetActor(r).(domain.User); ok {
		http.Redirect(w, r, "/panel/", http.StatusSeeOther)
		return
	}

	LoginPage("Login - Tracer").Render(r.Context(), w)
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		h.renderFormError(w, "Username and password are required")
		return
	}

	token, err := h.authService.Login(r.Context(), username, password)
	if err != nil {
		slog.Debug("Login failed", "username", username, "error", err)
		h.renderFormError(w, "Invalid credentials")
		return
	}

	authmw.SetSessionCookie(w, r, token, int(h.sessionMax.Seconds()))
	slog.Info("User logged in", "username", username)

	w.Header().Set("HX-Redirect", "/panel/")
	w.WriteHeader(http.StatusOK)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("tracer_session"); err == nil {
		h.authService.DeleteSession(r.Context(), cookie.Value)
	}

	authmw.ClearSessionCookie(w)
	authmw.RedirectToLogin(w, r)
}

// Setup handlers

func (h *handler) setupPage(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := h.authService.HasAnyUsers(r.Context())
	if err != nil {
		slog.Error("Failed to check users", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
		templates.ErrorPage("Error", "Failed to check users", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return
	}

	if hasUsers {
		authmw.RedirectToLogin(w, r)
		return
	}

	h.renderSetupPage(w, r, "")
}

func (h *handler) setup(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := h.authService.HasAnyUsers(r.Context())
	if err != nil {
		slog.Error("Failed to check users", "error", err)
		h.renderSetupPage(w, r, "Failed to check users: "+err.Error())
		return
	}

	if hasUsers {
		w.Header().Set("HX-Redirect", authmw.LoginPath(r))
		w.WriteHeader(http.StatusOK)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	confirmPassword := r.FormValue("confirm_password")

	if username == "" || password == "" {
		h.renderSetupPage(w, r, "Username and password are required")
		return
	}

	if password != confirmPassword {
		h.renderSetupPage(w, r, "Passwords do not match")
		return
	}

	registerReq := auth.RegisterRequest{
		Username: username,
		Password: password,
		IsAdmin:  true,
	}
	if err := registerReq.Validate(); err != nil {
		h.renderSetupPage(w, r, "Failed to create user: "+err.Error())
		return
	}

	user, err := h.authService.Register(r.Context(), registerReq)
	if err != nil {
		slog.Error("Failed to create admin user", "error", err)
		h.renderSetupPage(w, r, "Failed to create user: "+err.Error())
		return
	}

	slog.Info("Admin user created", "user_id", user.ID, "username", username)

	apiKeyReq := auth.GenerateCollectorAPIKeyRequest{
		Name:   "default",
		UserID: user.ID,
	}
	if err := apiKeyReq.Validate(); err != nil {
		h.renderSetupPage(w, r, "User created but failed to create collector API key: "+err.Error())
		return
	}

	created, err := h.authService.GenerateCollectorAPIKey(r.Context(), domain.System{}, apiKeyReq)
	if err != nil {
		slog.Error("Failed to create collector API key", "error", err)
		h.renderSetupPage(w, r, "User created but failed to create collector API key: "+err.Error())
		return
	}

	token, err := h.authService.CreateSession(r.Context(), user.ID)
	if err != nil {
		slog.Error("Failed to create session", "error", err)
		h.renderSetupPage(w, r, "User created but failed to create session: "+err.Error())
		return
	}

	authmw.SetSessionCookie(w, r, token, int(h.sessionMax.Seconds()))
	slog.Info("Admin user created and logged in", "username", username)

	// The page shows the collector API key plaintext (only once); never cache it.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	SetupSuccessPage("Setup Complete - Tracer", username, created.Plaintext).Render(r.Context(), w)
}
