package settings

import (
	"context"
	"embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/ai-shift/tracer/apps/panel/db"
	"github.com/ai-shift/tracer/apps/panel/internal/dbmanager"
	"github.com/ai-shift/tracer/apps/panel/internal/services/auth"
	"github.com/ai-shift/tracer/apps/panel/internal/services/ingest"
	"github.com/ai-shift/tracer/apps/panel/internal/services/notify"
	"github.com/ai-shift/tracer/apps/panel/internal/services/permissions"
	"github.com/ai-shift/tracer/apps/panel/internal/services/user"
	authmw "github.com/ai-shift/tracer/apps/panel/web/middleware/auth"
	"github.com/ai-shift/tracer/apps/panel/web/templates"
	sharedtypes "github.com/ai-shift/tracer/libs/types"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t             templates.Templates
	authService   *auth.Service
	userService   *user.Service
	ingestService *ingest.Service
	dbManager     *dbmanager.Manager
	notifyService *notify.Service
	permissions   *permissions.Service
}

func InitMux(authService *auth.Service, userService *user.Service, ingestService *ingest.Service, dbManager *dbmanager.Manager, notifyService *notify.Service, permissions *permissions.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:             templates.New(views),
		authService:   authService,
		userService:   userService,
		ingestService: ingestService,
		dbManager:     dbManager,
		notifyService: notifyService,
		permissions:   permissions,
	}

	// Users management
	m.HandleFunc("GET /", h.settingsPage)
	m.HandleFunc("GET /entries", h.settingsEntries)
	m.HandleFunc("GET /users", h.listUsers)
	m.HandleFunc("POST /users", h.createUser)
	m.HandleFunc("POST /users/{id}/delete", h.deleteUser)
	m.HandleFunc("GET /users/{id}/apps", h.getUserApps)
	m.HandleFunc("GET /users/{id}/apps/entries", h.getUserAppsEntries)
	m.HandleFunc("POST /users/{id}/apps", h.updateUserApps)

	// API key
	m.HandleFunc("GET /api-key", h.getAPIKey)
	m.HandleFunc("POST /api-key/regenerate", h.regenerateAPIKey)

	// User API keys (LLM compatibility)
	m.HandleFunc("POST /api-keys", h.createUserAPIKey)
	m.HandleFunc("POST /api-keys/{id}/delete", h.deleteUserAPIKey)

	// Notifiers management
	m.HandleFunc("GET /notifiers", h.listNotifiers)
	m.HandleFunc("POST /notifiers", h.createNotifier)
	m.HandleFunc("GET /notifiers/{id}", h.getNotifier)
	m.HandleFunc("GET /notifiers/{id}/entries", h.getNotifierEntries)
	m.HandleFunc("POST /notifiers/{id}", h.updateNotifier)
	m.HandleFunc("POST /notifiers/{id}/delete", h.deleteNotifier)
	m.HandleFunc("POST /notifiers/{id}/toggle", h.toggleNotifier)
	m.HandleFunc("GET /notifiers/{id}/apps", h.getNotifierApps)
	m.HandleFunc("GET /notifiers/{id}/apps/entries", h.getNotifierAppsEntries)
	m.HandleFunc("POST /notifiers/{id}/apps", h.updateNotifierApps)

	return m
}

func (h *handler) requireAuthenticatedUser(w http.ResponseWriter, r *http.Request) (sharedtypes.User, error) {
	user, err := authmw.GetUser(r)
	if err != nil {
		authmw.RedirectToLogin(w, r)
		return sharedtypes.User{}, err
	}
	return user, nil
}

func (h *handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	data, err := h.buildSettingsPageData(w, r)
	if err != nil {
		return
	}
	h.t.RenderHTTP(w, r, "settings", data)
}

func (h *handler) settingsEntries(w http.ResponseWriter, r *http.Request) {
	data, err := h.buildSettingsPageData(w, r)
	if err != nil {
		return
	}
	h.t.RenderHTTP(w, r, "settings-content", data)
}

func (h *handler) buildSettingsPageData(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return nil, http.ErrNoCookie
	}

	isAdmin := user.IsAdmin

	data := map[string]any{
		"Title":       "Settings - Tracer",
		"CurrentUser": user,
		"IsAdmin":     isAdmin,
		"Nav":         templates.NewNavData("Settings", "settings", user.Username),
	}

	// Admin-only data
	if isAdmin {
		users, err := h.userService.List(r.Context())
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to list users", http.StatusInternalServerError)
			return nil, err
		}
		data["Users"] = users

		apiKey, _ := h.authService.GetAPIKey(r.Context())
		data["APIKey"] = apiKey

		// Admins see all notifiers
		notifiers, err := h.notifyService.ListNotifiers(r.Context())
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to list notifiers", http.StatusInternalServerError)
			return nil, err
		}
		data["Notifiers"] = notifiers
	} else {
		// Non-admins only see their own notifiers
		notifiers, err := h.notifyService.ListNotifiersByUser(r.Context(), user.ID)
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to list notifiers", http.StatusInternalServerError)
			return nil, err
		}
		data["Notifiers"] = notifiers
	}

	// Load user's API keys (all users can have API keys)
	userAPIKeys, err := h.authService.ListUserAPIKeys(r.Context(), user.ID)
	if err != nil {
		slog.Error("Failed to list user API keys", "error", err)
	} else {
		data["UserAPIKeys"] = userAPIKeys
	}

	return data, nil
}

// User handlers

func (h *handler) listUsers(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireAdmin(w, r)
	if err != nil {
		return
	}

	users, err := h.userService.List(r.Context())
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list users", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Users":       users,
		"CurrentUser": user,
	}

	h.t.RenderHTTP(w, r, "users-list", data)
}

func (h *handler) createUser(w http.ResponseWriter, r *http.Request) {
	if _, err := authmw.RequireAdmin(w, r); err != nil {
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	isAdmin := r.FormValue("is_admin") == "on"

	if username == "" || password == "" {
		http.Error(w, "Username and password required", http.StatusBadRequest)
		return
	}

	_, err := h.authService.Register(r.Context(), username, password, isAdmin)
	if err != nil {
		slog.Error("Failed to create user", "error", err)
		http.Error(w, "Failed to create user", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	currentUser, err := authmw.RequireAdmin(w, r)
	if err != nil {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	if id == currentUser.ID {
		http.Error(w, "Cannot delete yourself", http.StatusBadRequest)
		return
	}

	if err := h.userService.Delete(r.Context(), id); err != nil {
		slog.Error("Failed to delete user", "error", err)
		http.Error(w, "Failed to delete user", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) getUserApps(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildUserAppsData(w, r)
	if !ok {
		return
	}
	h.t.RenderHTTP(w, r, "user-apps", data)
}

func (h *handler) getUserAppsEntries(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildUserAppsData(w, r)
	if !ok {
		return
	}
	h.t.RenderHTTP(w, r, "user-apps-content", data)
}

func (h *handler) buildUserAppsData(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	currentUser, err := authmw.RequireAdmin(w, r)
	if err != nil {
		return nil, false
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return nil, false
	}

	user, err := h.userService.GetByID(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "User not found", http.StatusNotFound)
		return nil, false
	}

	userApps, err := h.userService.GetApps(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get user apps", http.StatusInternalServerError)
		return nil, false
	}

	allApps, err := h.listAllApps(r.Context())
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list apps", http.StatusInternalServerError)
		return nil, false
	}

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
		"Nav":       templates.NewNavData("Manage App Access", "settings", currentUser.Username),
	}

	return data, true
}

func (h *handler) updateUserApps(w http.ResponseWriter, r *http.Request) {
	if _, err := authmw.RequireAdmin(w, r); err != nil {
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

	selectedApps := r.Form["apps"]
	currentApps, err := h.userService.GetApps(r.Context(), id)
	if err != nil {
		http.Error(w, "Failed to get user apps", http.StatusInternalServerError)
		return
	}

	selectedMap := make(map[string]bool)
	for _, app := range selectedApps {
		selectedMap[app] = true
	}

	currentMap := make(map[string]bool)
	for _, app := range currentApps {
		currentMap[app] = true
	}

	for _, app := range selectedApps {
		if !currentMap[app] {
			if err := h.userService.AddApp(r.Context(), id, app); err != nil {
				slog.Error("Failed to add user app", "user_id", id, "app", app, "error", err)
			}
		}
	}

	for _, app := range currentApps {
		if !selectedMap[app] {
			if err := h.userService.RemoveApp(r.Context(), id, app); err != nil {
				slog.Error("Failed to remove user app", "user_id", id, "app", app, "error", err)
			}
		}
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

// API key handlers

func (h *handler) getAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, err := authmw.RequireAdmin(w, r); err != nil {
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
	if _, err := authmw.RequireAdmin(w, r); err != nil {
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
	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

// User API key handlers (LLM compatibility)

func (h *handler) createUserAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "Name required", http.StatusBadRequest)
		return
	}

	_, err = h.authService.GenerateUserAPIKey(r.Context(), user.ID, name)
	if err != nil {
		slog.Error("Failed to generate user API key", "error", err)
		http.Error(w, "Failed to generate API key", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) deleteUserAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid API key ID", http.StatusBadRequest)
		return
	}

	// Get the key to verify ownership
	keys, err := h.authService.ListUserAPIKeys(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Failed to verify key ownership", http.StatusInternalServerError)
		return
	}

	// Check if user owns this key
	owned := false
	for _, k := range keys {
		if k.ID == id {
			owned = true
			break
		}
	}

	if !owned && !user.IsAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := h.authService.DeleteUserAPIKey(r.Context(), id); err != nil {
		slog.Error("Failed to delete user API key", "error", err)
		http.Error(w, "Failed to delete API key", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

// Notifier handlers

func (h *handler) listNotifiers(w http.ResponseWriter, r *http.Request) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	var notifiers []db.Notifier
	if user.IsAdmin {
		notifiers, err = h.notifyService.ListNotifiers(r.Context())
	} else {
		notifiers, err = h.notifyService.ListNotifiersByUser(r.Context(), user.ID)
	}
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list notifiers", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Notifiers": notifiers,
	}

	h.t.RenderHTTP(w, r, "notifiers-list", data)
}

func (h *handler) createNotifier(w http.ResponseWriter, r *http.Request) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	name := r.FormValue("name")
	notifierType := r.FormValue("type")

	if name == "" || notifierType == "" {
		http.Error(w, "Name and type required", http.StatusBadRequest)
		return
	}

	// Build request based on type
	req := notify.AddNotifierRequest{
		UserID:      user.ID,
		Name:        name,
		Type:        notifierType,
		InitialApps: []string{}, // No apps initially
	}

	switch notifierType {
	case "telegram":
		token := r.FormValue("telegram_token")
		chatIDStr := r.FormValue("telegram_chat_id")
		chatID, _ := strconv.ParseInt(chatIDStr, 10, 64)

		if token == "" || chatID == 0 {
			http.Error(w, "Telegram token and chat ID required", http.StatusBadRequest)
			return
		}

		req.Token = token
		req.ChatID = chatID
	default:
		http.Error(w, "Unknown notifier type", http.StatusBadRequest)
		return
	}

	// Create notifier through service (handles DB, welcome notification, registration)
	if err := h.notifyService.AddNotifier(r.Context(), req); err != nil {
		slog.Error("Failed to add notifier", "error", err)
		http.Error(w, "Failed to create notifier", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) getNotifier(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildNotifierData(w, r)
	if !ok {
		return
	}
	h.t.RenderHTTP(w, r, "notifier-edit", data)
}

func (h *handler) getNotifierEntries(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildNotifierData(w, r)
	if !ok {
		return
	}
	h.t.RenderHTTP(w, r, "notifier-edit-content", data)
}

func (h *handler) buildNotifierData(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return nil, false
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return nil, false
	}

	notifier, err := h.notifyService.GetNotifierDB(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Notifier not found", http.StatusNotFound)
		return nil, false
	}

	// Check ownership (admins can access any, others only their own)
	if !h.permissions.CanManageNotifier(user, notifier.UserID) {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this notifier", http.StatusForbidden)
		return nil, false
	}

	subscriptions, err := h.notifyService.GetNotifierApps(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get notifier subscriptions", http.StatusInternalServerError)
		return nil, false
	}

	// Get available apps based on user access
	allApps, err := h.getAvailableApps(r.Context(), user)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list apps", http.StatusInternalServerError)
		return nil, false
	}

	// Build subscription map with "collector:app" keys for display
	subscriptionMap := make(map[string]bool)
	for _, sub := range subscriptions {
		key := sub.CollectorName + ":" + sub.AppName
		subscriptionMap[key] = true
	}

	data := map[string]any{
		"Title":         "Edit Notifier - Tracer",
		"Notifier":      notifier,
		"Subscriptions": subscriptionMap,
		"AllApps":       allApps,
		"IsAdmin":       user.IsAdmin,
	}

	return data, true
}

func (h *handler) updateNotifier(w http.ResponseWriter, r *http.Request) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return
	}

	notifier, err := h.notifyService.GetNotifierDB(r.Context(), id)
	if err != nil {
		http.Error(w, "Notifier not found", http.StatusNotFound)
		return
	}

	// Check ownership
	if !h.permissions.CanManageNotifier(user, notifier.UserID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		name = notifier.Name
	}

	var configJSON string
	switch notifier.Type {
	case "telegram":
		token := r.FormValue("telegram_token")
		chatIDStr := r.FormValue("telegram_chat_id")
		chatID, _ := strconv.ParseInt(chatIDStr, 10, 64)

		if token == "" || chatID == 0 {
			http.Error(w, "Telegram token and chat ID required", http.StatusBadRequest)
			return
		}

		cfg := notify.TelegramConfig{Token: token, ChatID: chatID}
		cfgBytes, _ := json.Marshal(cfg)
		configJSON = string(cfgBytes)
	default:
		configJSON = notifier.Config
	}

	enabled := notifier.Enabled == 1

	if err := h.notifyService.UpdateNotifier(r.Context(), id, name, notifier.Type, configJSON, enabled); err != nil {
		slog.Error("Failed to update notifier", "error", err)
		http.Error(w, "Failed to update notifier", http.StatusInternalServerError)
		return
	}

	h.notifyService.RegisterNotifierWithUser(r.Context(), id, user)

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) deleteNotifier(w http.ResponseWriter, r *http.Request) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return
	}

	notifier, err := h.notifyService.GetNotifierDB(r.Context(), id)
	if err != nil {
		http.Error(w, "Notifier not found", http.StatusNotFound)
		return
	}

	// Check ownership
	if !h.permissions.CanManageNotifier(user, notifier.UserID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := h.notifyService.DeleteNotifier(r.Context(), id); err != nil {
		slog.Error("Failed to delete notifier", "error", err)
		http.Error(w, "Failed to delete notifier", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) toggleNotifier(w http.ResponseWriter, r *http.Request) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return
	}

	notifier, err := h.notifyService.GetNotifierDB(r.Context(), id)
	if err != nil {
		http.Error(w, "Notifier not found", http.StatusNotFound)
		return
	}

	// Check ownership
	if !h.permissions.CanManageNotifier(user, notifier.UserID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if notifier.Enabled == 1 {
		err = h.notifyService.DisableNotifier(r.Context(), id)
	} else {
		err = h.notifyService.EnableNotifier(r.Context(), id)
	}

	if err != nil {
		slog.Error("Failed to toggle notifier", "error", err)
		http.Error(w, "Failed to toggle notifier", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) getNotifierApps(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildNotifierAppsData(w, r)
	if !ok {
		return
	}
	h.t.RenderHTTP(w, r, "notifier-apps", data)
}

func (h *handler) getNotifierAppsEntries(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildNotifierAppsData(w, r)
	if !ok {
		return
	}
	h.t.RenderHTTP(w, r, "notifier-apps-content", data)
}

func (h *handler) buildNotifierAppsData(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return nil, false
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return nil, false
	}

	notifier, err := h.notifyService.GetNotifierDB(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Notifier not found", http.StatusNotFound)
		return nil, false
	}

	// Check ownership
	if !h.permissions.CanManageNotifier(user, notifier.UserID) {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this notifier", http.StatusForbidden)
		return nil, false
	}

	// Get subscriptions with collector info
	subscriptions, err := h.notifyService.GetNotifierApps(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get notifier subscriptions", http.StatusInternalServerError)
		return nil, false
	}

	// Build subscription map with "collector:app" keys
	subscriptionMap := make(map[string]bool)
	for _, sub := range subscriptions {
		key := sub.CollectorName + ":" + sub.AppName
		subscriptionMap[key] = true
	}

	// Get app-collector mappings from filesystem
	collectors, err := h.ingestService.ListCollectors()
	if err != nil {
		slog.Error("Failed to list collectors", "error", err)
		h.t.RenderError(w, r, "Error", "Failed to list collectors", http.StatusInternalServerError)
		return nil, false
	}

	// Build structured data: map[collector][]app
	collectorApps := make(map[string][]string)
	for _, collector := range collectors {
		apps, err := h.ingestService.ListApps(collector)
		if err != nil {
			slog.Error("Failed to list apps for collector", "collector", collector, "error", err)
			h.t.RenderError(w, r, "Error", "Failed to list apps", http.StatusInternalServerError)
			return nil, false
		}
		collectorApps[collector.String()] = apps
	}

	// Filter by user access for non-admins
	if !user.IsAdmin {
		userApps, err := h.userService.GetApps(r.Context(), user.ID)
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to get user apps", http.StatusInternalServerError)
			return nil, false
		}
		userAppSet := make(map[string]bool)
		for _, app := range userApps {
			userAppSet[app] = true
		}

		// Filter collector apps to only show apps user has access to
		filtered := make(map[string][]string)
		for collector, apps := range collectorApps {
			var allowedApps []string
			for _, app := range apps {
				if userAppSet[app] {
					allowedApps = append(allowedApps, app)
				}
			}
			if len(allowedApps) > 0 {
				filtered[collector] = allowedApps
			}
		}
		collectorApps = filtered
	}

	data := map[string]any{
		"Title":         "Edit Notifier Apps - Tracer",
		"Notifier":      notifier,
		"NotifierID":    id,
		"Subscriptions": subscriptionMap, // map[string]bool (key: "collector:app")
		"CollectorApps": collectorApps,   // map[string][]string
		"IsAdmin":       user.IsAdmin,
		"Nav":           templates.NewNavData("Configure Notifier Apps", "settings", user.Username),
	}

	return data, true
}

func (h *handler) updateNotifierApps(w http.ResponseWriter, r *http.Request) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return
	}

	notifier, err := h.notifyService.GetNotifierDB(r.Context(), id)
	if err != nil {
		http.Error(w, "Notifier not found", http.StatusNotFound)
		return
	}

	// Check ownership
	if !h.permissions.CanManageNotifier(user, notifier.UserID) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	// Parse form values: "collector:app" format
	selectedPairs := r.Form["apps"]
	allApps := r.FormValue("all_apps") == "on"

	var subscriptions []notify.NotifierAppSubscription

	if allApps && user.IsAdmin {
		// Global wildcard subscription (all apps from all collectors)
		subscriptions = append(subscriptions, notify.NotifierAppSubscription{
			AppName:       "*",
			CollectorName: "*",
		})
	} else {
		// Parse collector:app pairs
		for _, pair := range selectedPairs {
			parts := strings.Split(pair, ":")
			if len(parts) != 2 {
				continue // Skip invalid format
			}
			subscriptions = append(subscriptions, notify.NotifierAppSubscription{
				CollectorName: parts[0],
				AppName:       parts[1],
			})
		}

		// For non-admins: filter to only apps they have access to
		if !user.IsAdmin {
			userApps, err := h.userService.GetApps(r.Context(), user.ID)
			if err != nil {
				http.Error(w, "Failed to get user apps", http.StatusInternalServerError)
				return
			}
			userAppSet := make(map[string]bool)
			for _, app := range userApps {
				userAppSet[app] = true
			}

			var filtered []notify.NotifierAppSubscription
			for _, sub := range subscriptions {
				// Allow wildcards or check app access
				if sub.AppName == "*" || userAppSet[sub.AppName] {
					filtered = append(filtered, sub)
				}
			}
			subscriptions = filtered
		}
	}

	// Update subscriptions through service (handles DB, subscription notifications, re-registration)
	if err := h.notifyService.UpdateNotifierApps(r.Context(), id, user.ID, subscriptions); err != nil {
		slog.Error("Failed to update notifier subscriptions", "error", err)
		http.Error(w, "Failed to update notifier subscriptions", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

// listAllApps returns all unique app names across all collectors
func (h *handler) listAllApps(ctx context.Context) ([]string, error) {
	collectors, err := h.ingestService.ListCollectors()
	if err != nil {
		return nil, err
	}

	appSet := make(map[string]struct{})
	for _, collector := range collectors {
		apps, err := h.ingestService.ListApps(collector)
		if err != nil {
			return nil, err
		}
		for _, app := range apps {
			appSet[app] = struct{}{}
		}
	}

	result := make([]string, 0, len(appSet))
	for app := range appSet {
		result = append(result, app)
	}
	return result, nil
}

// getAvailableApps returns apps available to a user (all apps for admins, user's apps for others)
func (h *handler) getAvailableApps(ctx context.Context, user sharedtypes.User) ([]string, error) {
	allApps, err := h.listAllApps(ctx)
	if err != nil {
		return nil, err
	}

	if user.IsAdmin {
		return allApps, nil
	}

	// Non-admins only see their allowed apps
	userApps, err := h.userService.GetApps(ctx, user.ID)
	if err != nil {
		return nil, err
	}

	userAppMap := make(map[string]bool)
	for _, app := range userApps {
		userAppMap[app] = true
	}

	var filtered []string
	for _, app := range allApps {
		if userAppMap[app] {
			filtered = append(filtered, app)
		}
	}
	return filtered, nil
}
