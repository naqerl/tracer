package settings

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/notify"
	"tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/web/htmx"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/templates"
	sharedtypes "tracer/libs/types"
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
}

func InitMux(authService *auth.Service, userService *user.Service, ingestService *ingest.Service, dbManager *dbmanager.Manager, notifyService *notify.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:             templates.New(views),
		authService:   authService,
		userService:   userService,
		ingestService: ingestService,
		dbManager:     dbManager,
		notifyService: notifyService,
	}

	// Users management
	m.HandleFunc("GET /", h.settingsPage)
	m.HandleFunc("POST /users", h.createUser)
	m.HandleFunc("DELETE /users/{id}", h.deleteUser)
	m.HandleFunc("GET /users/{id}/apps", h.getUserApps)
	m.HandleFunc("PUT /users/{id}/apps", h.updateUserApps)

	// API key
	m.HandleFunc("GET /api-key", h.getAPIKey)
	m.HandleFunc("PUT /api-key", h.regenerateAPIKey)

	// User API keys (LLM compatibility)
	m.HandleFunc("POST /api-keys", h.createUserAPIKey)
	m.HandleFunc("DELETE /api-keys/{id}", h.deleteUserAPIKey)

	// Notifiers management
	m.HandleFunc("POST /notifiers", h.createNotifier)
	m.HandleFunc("DELETE /notifiers/{id}", h.deleteNotifier)
	m.HandleFunc("PATCH /notifiers/{id}", h.toggleNotifier)
	m.HandleFunc("GET /notifiers/{id}/apps", h.getNotifierApps)
	m.HandleFunc("PUT /notifiers/{id}/apps", h.updateNotifierApps)

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

	htmx.Redirect(w, r, "/settings/")
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

	htmx.Redirect(w, r, "/settings/")
}

func (h *handler) getUserApps(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildUserAppsData(w, r)
	if !ok {
		return
	}
	h.t.RenderHTTP(w, r, "user-apps", data)
}

func (h *handler) buildUserAppsData(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	_, err := authmw.RequireAdmin(w, r)
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

	// Build user apps map
	userAppMap := make(map[string]bool)
	for _, app := range userApps {
		userAppMap[app] = true
	}

	// Get apps grouped by collector
	collectorApps, err := h.listAppsByCollector(r.Context())
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list apps", http.StatusInternalServerError)
		return nil, false
	}

	// Check if there are any apps at all
	hasNoApps := true
	for _, apps := range collectorApps {
		if len(apps) > 0 {
			hasNoApps = false
			break
		}
	}

	data := map[string]any{
		"Title":         "Edit User Apps - Tracer",
		"User":          user,
		"UserApps":      userAppMap,
		"CollectorApps": collectorApps,
		"HasNoApps":     hasNoApps,
	}

	return data, true
}

// listAppsByCollector returns all apps grouped by collector name
func (h *handler) listAppsByCollector(ctx context.Context) (map[string][]string, error) {
	collectors, err := h.ingestService.ListCollectors()
	if err != nil {
		return nil, err
	}

	collectorApps := make(map[string][]string)
	for _, collector := range collectors {
		apps, err := h.ingestService.ListApps(collector)
		if err != nil {
			return nil, err
		}
		collectorApps[collector] = apps
	}

	return collectorApps, nil
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

	// Parse form values: "collector:app" format
	selectedPairs := r.Form["apps"]
	currentApps, err := h.userService.GetApps(r.Context(), id)
	if err != nil {
		http.Error(w, "Failed to get user apps", http.StatusInternalServerError)
		return
	}

	// Extract app names from "collector:app" pairs
	selectedApps := make([]string, 0, len(selectedPairs))
	for _, pair := range selectedPairs {
		parts := strings.Split(pair, ":")
		if len(parts) == 2 {
			selectedApps = append(selectedApps, parts[1]) // app name is the second part
		}
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

	htmx.Redirect(w, r, "/settings/")
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
	htmx.Redirect(w, r, "/settings/")
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

	htmx.Redirect(w, r, "/settings/")
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

	htmx.Redirect(w, r, "/settings/")
}

// Notifier handlers

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

	// Build config JSON based on type
	var configJSON string
	switch notifierType {
	case "telegram":
		token := r.FormValue("telegram_token")
		chatIDStr := r.FormValue("telegram_chat_id")
		chatID, _ := strconv.ParseInt(chatIDStr, 10, 64)

		if token == "" || chatID == 0 {
			http.Error(w, "Telegram token and chat ID required", http.StatusBadRequest)
			return
		}

		cfg := struct {
			Token  string `json:"token"`
			ChatID int64  `json:"chat_id"`
		}{
			Token:  token,
			ChatID: chatID,
		}
		cfgBytes, err := json.Marshal(cfg)
		if err != nil {
			slog.Error("Failed to marshal telegram config", "error", err)
			http.Error(w, "Failed to process config", http.StatusInternalServerError)
			return
		}
		configJSON = string(cfgBytes)
	default:
		http.Error(w, "Unknown notifier type", http.StatusBadRequest)
		return
	}

	req := notify.AddNotifierRequest{
		UserID: user.ID,
		Name:   name,
		Type:   notifierType,
		Config: configJSON,
	}

	// Create notifier through service (handles DB, welcome notification, registration)
	if err := h.notifyService.AddNotifier(r.Context(), req); err != nil {
		slog.Error("Failed to add notifier", "error", err)
		http.Error(w, "Failed to create notifier", http.StatusInternalServerError)
		return
	}

	htmx.Redirect(w, r, "/settings/")
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

	if err := h.notifyService.DeleteNotifier(r.Context(), user, id); err != nil {
		switch {
		case errors.Is(err, notify.ErrNotifierNotFound):
			http.Error(w, "Notifier not found", http.StatusNotFound)
		case errors.Is(err, notify.ErrAccessDenied):
			http.Error(w, "Forbidden", http.StatusForbidden)
		default:
			slog.Error("Failed to delete notifier", "error", err)
			http.Error(w, "Failed to delete notifier", http.StatusInternalServerError)
		}
		return
	}

	htmx.Redirect(w, r, "/settings/")
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

	// Get current state to determine enable/disable
	setup, err := h.notifyService.GetSetup(r.Context(), user, id)
	if err != nil {
		switch {
		case errors.Is(err, notify.ErrNotifierNotFound):
			http.Error(w, "Notifier not found", http.StatusNotFound)
		case errors.Is(err, notify.ErrAccessDenied):
			http.Error(w, "Forbidden", http.StatusForbidden)
		default:
			slog.Error("Failed to get notifier", "error", err)
			http.Error(w, "Failed to toggle notifier", http.StatusInternalServerError)
		}
		return
	}

	if err := h.notifyService.ToggleNotifier(r.Context(), user, id, setup.Enabled != 1); err != nil {
		switch {
		case errors.Is(err, notify.ErrNotifierNotFound):
			http.Error(w, "Notifier not found", http.StatusNotFound)
		case errors.Is(err, notify.ErrAccessDenied):
			http.Error(w, "Forbidden", http.StatusForbidden)
		default:
			slog.Error("Failed to toggle notifier", "error", err)
			http.Error(w, "Failed to toggle notifier", http.StatusInternalServerError)
		}
		return
	}

	htmx.Redirect(w, r, "/settings/")
}

func (h *handler) getNotifierApps(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildNotifierAppsData(w, r)
	if !ok {
		return
	}
	h.t.RenderHTTP(w, r, "notifier-apps", data)
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

	setup, err := h.notifyService.GetSetup(r.Context(), user, id)
	if err != nil {
		switch {
		case errors.Is(err, notify.ErrNotifierNotFound):
			h.t.RenderError(w, r, "Error", "Notifier not found", http.StatusNotFound)
		case errors.Is(err, notify.ErrAccessDenied):
			h.t.RenderError(w, r, "Forbidden", "You don't have access to this notifier", http.StatusForbidden)
		default:
			h.t.RenderError(w, r, "Error", "Failed to get notifier", http.StatusInternalServerError)
		}
		return nil, false
	}

	// Build subscription map with "collector:app" keys
	subscriptionMap := make(map[string]bool)
	for _, sub := range setup.Subscriptions {
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
		collectorApps[collector] = apps
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
		"Notifier":      setup,
		"NotifierID":    id,
		"Subscriptions": subscriptionMap, // map[string]bool (key: "collector:app")
		"CollectorApps": collectorApps,   // map[string][]string
		"IsAdmin":       user.IsAdmin,
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
	if err := h.notifyService.UpdateNotifierApps(r.Context(), user, id, subscriptions); err != nil {
		switch {
		case errors.Is(err, notify.ErrNotifierNotFound):
			http.Error(w, "Notifier not found", http.StatusNotFound)
		case errors.Is(err, notify.ErrAccessDenied):
			http.Error(w, "Forbidden", http.StatusForbidden)
		default:
			slog.Error("Failed to update notifier subscriptions", "error", err)
			http.Error(w, "Failed to update notifier subscriptions", http.StatusInternalServerError)
		}
		return
	}

	htmx.Redirect(w, r, "/settings/")
}
