package users

import (
	"embed"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ai-shift/tracer/core/services/auth"
	"github.com/ai-shift/tracer/core/services/dbmanager"
	"github.com/ai-shift/tracer/web/htmx"
	"github.com/ai-shift/tracer/web/middleware"
	"github.com/ai-shift/tracer/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t           templates.Templates
	authService *auth.Service
	dbManager   *dbmanager.Manager
}

func InitMux(authService *auth.Service, dbManager *dbmanager.Manager) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:           templates.New(views),
		authService: authService,
		dbManager:   dbManager,
	}

	m.HandleFunc("GET /", h.listUsers)
	m.HandleFunc("POST /", h.createUser)
	m.HandleFunc("POST /{id}/delete", h.deleteUser)
	m.HandleFunc("GET /{id}/apps", h.getUserApps)
	m.HandleFunc("POST /{id}/apps", h.updateUserApps)
	m.HandleFunc("GET /api-key", h.getAPIKey)
	m.HandleFunc("POST /api-key/regenerate", h.regenerateAPIKey)

	return m
}

func (h *handler) listUsers(w http.ResponseWriter, r *http.Request) {
	user := middleware.RequireAdmin(w, r)
	if user == nil {
		return
	}

	users, err := h.authService.ListUsers(r.Context())
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list users", http.StatusInternalServerError)
		return
	}

	apiKey, _ := h.authService.GetAPIKey(r.Context())

	data := map[string]any{
		"Title":       "User Management - Tracer",
		"Users":       users,
		"CurrentUser": user,
		"APIKey":      apiKey,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "users-content", data)
	} else {
		h.t.RenderHTTP(w, r, "users", data)
	}
}

func (h *handler) createUser(w http.ResponseWriter, r *http.Request) {
	if middleware.RequireAdmin(w, r) == nil {
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	isAdmin := r.FormValue("is_admin") == "on"

	if username == "" || password == "" {
		http.Error(w, "Username and password required", http.StatusBadRequest)
		return
	}

	_, err := h.authService.CreateUser(r.Context(), username, password, isAdmin)
	if err != nil {
		slog.Error("Failed to create user", "error", err)
		http.Error(w, "Failed to create user", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/users/", http.StatusSeeOther)
}

func (h *handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	currentUser := middleware.RequireAdmin(w, r)
	if currentUser == nil {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	// Prevent self-deletion
	if id == currentUser.ID {
		http.Error(w, "Cannot delete yourself", http.StatusBadRequest)
		return
	}

	if err := h.authService.DeleteUser(r.Context(), id); err != nil {
		slog.Error("Failed to delete user", "error", err)
		http.Error(w, "Failed to delete user", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/users/", http.StatusSeeOther)
}

func (h *handler) getUserApps(w http.ResponseWriter, r *http.Request) {
	if middleware.RequireAdmin(w, r) == nil {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	user, err := h.authService.GetUserByID(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "User not found", http.StatusNotFound)
		return
	}

	userApps, err := h.authService.GetUserApps(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get user apps", http.StatusInternalServerError)
		return
	}

	allApps, err := h.dbManager.ListApps()
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list apps", http.StatusInternalServerError)
		return
	}

	// Create a map of apps the user has access to
	userAppMap := make(map[string]bool)
	for _, app := range userApps {
		userAppMap[app] = true
	}

	data := map[string]any{
		"Title":     "Edit User Apps - Tracer",
		"User":      user,
		"UserApps":  userAppMap,
		"AllApps":   allApps,
		"HasNoApps": len(allApps) == 0,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "user-apps-content", data)
	} else {
		h.t.RenderHTTP(w, r, "user-apps", data)
	}
}

func (h *handler) updateUserApps(w http.ResponseWriter, r *http.Request) {
	if middleware.RequireAdmin(w, r) == nil {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	// Get all apps from the form
	selectedApps := r.Form["apps"]

	// Get current user apps
	currentApps, err := h.authService.GetUserApps(r.Context(), id)
	if err != nil {
		http.Error(w, "Failed to get user apps", http.StatusInternalServerError)
		return
	}

	// Create maps for easier comparison
	selectedMap := make(map[string]bool)
	for _, app := range selectedApps {
		selectedMap[app] = true
	}

	currentMap := make(map[string]bool)
	for _, app := range currentApps {
		currentMap[app] = true
	}

	// Add new apps
	for _, app := range selectedApps {
		if !currentMap[app] {
			if err := h.authService.AddUserApp(r.Context(), id, app); err != nil {
				slog.Error("Failed to add user app", "user_id", id, "app", app, "error", err)
			}
		}
	}

	// Remove deselected apps
	for _, app := range currentApps {
		if !selectedMap[app] {
			if err := h.authService.RemoveUserApp(r.Context(), id, app); err != nil {
				slog.Error("Failed to remove user app", "user_id", id, "app", app, "error", err)
			}
		}
	}

	http.Redirect(w, r, "/users/", http.StatusSeeOther)
}

func (h *handler) getAPIKey(w http.ResponseWriter, r *http.Request) {
	if middleware.RequireAdmin(w, r) == nil {
		return
	}

	apiKey, err := h.authService.GetAPIKey(r.Context())
	if err != nil {
		http.Error(w, "Failed to get API key", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"APIKey": apiKey,
	}

	h.t.RenderHTTP(w, r, "api-key-partial", data)
}

func (h *handler) regenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	if middleware.RequireAdmin(w, r) == nil {
		return
	}

	newKey, err := auth.GenerateAPIKey()
	if err != nil {
		http.Error(w, "Failed to generate API key", http.StatusInternalServerError)
		return
	}

	if err := h.authService.SetAPIKey(r.Context(), newKey); err != nil {
		http.Error(w, "Failed to save API key", http.StatusInternalServerError)
		return
	}

	slog.Info("API key regenerated")
	http.Redirect(w, r, "/users/", http.StatusSeeOther)
}
