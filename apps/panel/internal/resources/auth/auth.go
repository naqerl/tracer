package auth

import (
	"embed"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/ai-shift/tracer/apps/panel/internal/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/middleware"
	"github.com/ai-shift/tracer/apps/panel/internal/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t           templates.Templates
	authService *auth.Service
	sessionMax  int
}

func InitMux(authService *auth.Service, sessionDurationSec int) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:           templates.New(views),
		authService: authService,
		sessionMax:  sessionDurationSec,
	}

	// Login routes
	m.HandleFunc("GET /login", h.loginPage)
	m.HandleFunc("POST /login", h.login)
	m.HandleFunc("POST /logout", h.logout)

	// Setup routes
	m.HandleFunc("GET /setup", h.setupPage)
	m.HandleFunc("POST /setup", h.setup)

	return m
}

// renderFormError renders an error message for HTMX form submission
func (h *handler) renderFormError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	fmt.Fprintf(w, `<div class="error">%s</div>`, msg)
}

// Login handlers

func (h *handler) loginPage(w http.ResponseWriter, r *http.Request) {
	if user := middleware.GetUser(r); user != nil {
		http.Redirect(w, r, "/analytics/", http.StatusSeeOther)
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

	user, token, err := h.authService.Login(r.Context(), username, password)
	if err != nil {
		slog.Debug("Login failed", "username", username, "error", err)
		h.renderFormError(w, "Invalid credentials")
		return
	}

	middleware.SetSessionCookie(w, token, h.sessionMax)
	slog.Info("User logged in", "username", user.Username)

	w.Header().Set("HX-Redirect", "/analytics/")
	w.WriteHeader(http.StatusOK)
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("tracer_session"); err == nil {
		h.authService.DeleteSession(r.Context(), cookie.Value)
	}

	middleware.ClearSessionCookie(w)
	middleware.RedirectToLogin(w, r)
}

// Setup handlers

func (h *handler) setupPage(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := h.authService.HasUsers(r.Context())
	if err != nil {
		slog.Error("Failed to check users", "error", err)
		h.t.RenderError(w, r, "Error", "Failed to check users", http.StatusInternalServerError)
		return
	}

	if hasUsers {
		middleware.RedirectToLogin(w, r)
		return
	}

	apiKey, err := h.authService.EnsureAPIKey(r.Context())
	if err != nil {
		slog.Error("Failed to get API key", "error", err)
		h.t.RenderError(w, r, "Error", "Failed to get API key", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":  "Setup - Tracer",
		"APIKey": apiKey,
	}
	h.t.RenderHTTP(w, r, "setup", data)
}

func (h *handler) setup(w http.ResponseWriter, r *http.Request) {
	hasUsers, err := h.authService.HasUsers(r.Context())
	if err != nil {
		slog.Error("Failed to check users", "error", err)
		h.renderFormError(w, "Failed to check users: "+err.Error())
		return
	}

	if hasUsers {
		w.Header().Set("HX-Redirect", middleware.LoginPath(r))
		w.WriteHeader(http.StatusOK)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	confirmPassword := r.FormValue("confirm_password")

	if username == "" || password == "" {
		h.renderFormError(w, "Username and password are required")
		return
	}

	if password != confirmPassword {
		h.renderFormError(w, "Passwords do not match")
		return
	}

	user, err := h.authService.CreateUser(r.Context(), username, password, true)
	if err != nil {
		slog.Error("Failed to create admin user", "error", err)
		h.renderFormError(w, "Failed to create user: "+err.Error())
		return
	}

	slog.Info("Admin user created", "user_id", user.ID, "username", username)

	_, err = h.authService.EnsureAPIKey(r.Context())
	if err != nil {
		slog.Error("Failed to ensure API key", "error", err)
	}

	token, err := h.authService.CreateSession(r.Context(), user.ID)
	if err != nil {
		slog.Error("Failed to create session", "error", err)
		h.renderFormError(w, "User created but failed to create session: "+err.Error())
		return
	}

	middleware.SetSessionCookie(w, token, h.sessionMax)
	slog.Info("Admin user created and logged in", "username", username)

	w.Header().Set("HX-Redirect", "/analytics/")
	w.WriteHeader(http.StatusOK)
}
