package auth

import (
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	authsvc "tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/user"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/middleware/ratelimit"
	"tracer/apps/panel/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t           templates.Templates
	authService *authsvc.Service
	userService *user.Service
	sessionMax  time.Duration
}

func InitMux(authService *authsvc.Service, userService *user.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:           templates.New(views),
		authService: authService,
		userService: userService,
		sessionMax:  authsvc.SessionDuration,
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
	data := map[string]any{
		"Title": "Setup - Tracer",
		"Error": errorMessage,
	}
	h.t.RenderHTTP(w, r, "setup", data)
}

// Login handlers

func (h *handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, err := authmw.GetUserID(r); err == nil {
		http.Redirect(w, r, "/panel/", http.StatusSeeOther)
		return
	}

	data := map[string]any{
		"Title": "Login - Tracer",
	}
	h.t.RenderHTTP(w, r, "login", data)
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
		h.t.RenderError(w, r, "Error", "Failed to check users", http.StatusInternalServerError)
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

	registerReq := authsvc.RegisterRequest{
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

	apiKeyReq := authsvc.GenerateCollectorAPIKeyRequest{
		Name: "default",
	}
	if err := apiKeyReq.Validate(); err != nil {
		h.renderSetupPage(w, r, "User created but failed to create collector API key: "+err.Error())
		return
	}

	collectorAPIKey, _, err := h.authService.GenerateCollectorAPIKey(r.Context(), apiKeyReq)
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

	h.t.RenderHTTP(w, r, "setup-success", map[string]any{
		"Title":           "Setup Complete - Tracer",
		"Username":        username,
		"CollectorAPIKey": collectorAPIKey,
	})
}
