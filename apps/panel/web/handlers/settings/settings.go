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
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/web/htmx"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t             templates.Templates
	authService   *auth.Service
	userService   *usersvc.Service
	ingestService *ingest.Service
	dbManager     *dbmanager.Manager
	notifyService *notify.Service
}

func appKey(collectorName, appName string) string {
	return collectorName + ":" + appName
}

func InitMux(authService *auth.Service, userService *usersvc.Service, ingestService *ingest.Service, dbManager *dbmanager.Manager, notifyService *notify.Service) *http.ServeMux {
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

	// Collector API keys
	m.HandleFunc("POST /admin/collector-api-keys", h.createCollectorAPIKey)
	m.HandleFunc("DELETE /admin/collector-api-keys/{name}", h.deleteCollectorAPIKey)

	// User API keys (LLM compatibility)
	m.HandleFunc("POST /api-keys", h.createAPIKey)
	m.HandleFunc("DELETE /api-keys/{id}", h.deleteAPIKey)

	// Notifiers management
	m.HandleFunc("POST /notifiers", h.createNotifier)
	m.HandleFunc("DELETE /notifiers/{id}", h.deleteNotifier)
	m.HandleFunc("PUT /notifiers/{id}/toggle", h.toggleNotifier)
	m.HandleFunc("GET /notifiers/{id}/subscriptions", h.getNotifierSubscriptions)
	m.HandleFunc("PUT /notifiers/{id}/subscriptions", h.updateNotifierSubscriptions)

	return m
}

func (h *handler) requireAuthenticatedUser(w http.ResponseWriter, r *http.Request) (usersvc.User, error) {
	userID, err := authmw.GetUserID(r)
	if err != nil {
		authmw.RedirectToLogin(w, r)
		return usersvc.User{}, err
	}
	return h.userService.FindByID(r.Context(), userID)
}

func (h *handler) requireAdminUser(w http.ResponseWriter, r *http.Request) (usersvc.User, error) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return usersvc.User{}, err
	}
	if !user.IsAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return usersvc.User{}, authmw.ErrForbidden
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

		collectorAPIKeys, err := h.authService.ListCollectorAPIKeys(r.Context())
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to list collector API keys", http.StatusInternalServerError)
			return nil, err
		}
		data["CollectorAPIKeys"] = collectorAPIKeys
	}

	// Load notifiers (admins see all, non-admins see only their own)
	notifiers, err := h.notifyService.List(r.Context(), user)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list notifiers", http.StatusInternalServerError)
		return nil, err
	}
	data["Notifiers"] = notifiers

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
	if _, err := h.requireAdminUser(w, r); err != nil {
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
	currentUser, err := h.requireAdminUser(w, r)
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
	_, err := h.requireAdminUser(w, r)
	if err != nil {
		return nil, false
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return nil, false
	}

	user, err := h.userService.FindByID(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "User not found", http.StatusNotFound)
		return nil, false
	}

	userApps, err := h.userService.FindAccessPolicy(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get user apps", http.StatusInternalServerError)
		return nil, false
	}

	// Build user apps map keyed by "collector:app".
	userAppMap := make(map[string]bool)
	for _, app := range userApps.Apps {
		userAppMap[appKey(app.CollectorName, app.AppName)] = true
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
	if _, err := h.requireAdminUser(w, r); err != nil {
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
	currentApps, err := h.userService.FindAccessPolicy(r.Context(), id)
	if err != nil {
		http.Error(w, "Failed to get user apps", http.StatusInternalServerError)
		return
	}

	selectedMap := make(map[string]bool, len(selectedPairs))
	for _, pair := range selectedPairs {
		parts := strings.Split(pair, ":")
		if len(parts) == 2 {
			selectedMap[pair] = true
		}
	}

	currentMap := make(map[string]bool)
	for _, app := range currentApps.Apps {
		currentMap[appKey(app.CollectorName, app.AppName)] = true
	}

	for pair := range selectedMap {
		if currentMap[pair] {
			continue
		}
		parts := strings.Split(pair, ":")
		if len(parts) != 2 {
			continue
		}
		if err := h.userService.GrantAccess(r.Context(), id, usersvc.AppAccess{CollectorName: parts[0], AppName: parts[1]}); err != nil {
			slog.Error("Failed to add user app", "user_id", id, "collector", parts[0], "app", parts[1], "error", err)
		}
	}

	for _, app := range currentApps.Apps {
		pair := appKey(app.CollectorName, app.AppName)
		if !selectedMap[pair] {
			if err := h.userService.RevokeAccess(r.Context(), id, usersvc.AppAccess{CollectorName: app.CollectorName, AppName: app.AppName}); err != nil {
				slog.Error("Failed to remove user app", "user_id", id, "collector", app.CollectorName, "app", app.AppName, "error", err)
			}
		}
	}

	htmx.Redirect(w, r, "/settings/")
}

// Collector API key handlers

func (h *handler) createCollectorAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdminUser(w, r); err != nil {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "Name required", http.StatusBadRequest)
		return
	}

	newKey, _, err := h.authService.GenerateCollectorAPIKey(r.Context(), name)
	if err != nil {
		slog.Error("Failed to generate collector API key", "error", err)
		http.Error(w, "Failed to generate collector API key", http.StatusInternalServerError)
		return
	}

	data, buildErr := h.buildSettingsPageData(w, r)
	if buildErr != nil {
		return
	}
	data["NewCollectorAPIKey"] = newKey
	data["NewCollectorAPIKeyName"] = name

	h.t.RenderHTTP(w, r, "settings", data)
}

func (h *handler) deleteCollectorAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdminUser(w, r); err != nil {
		return
	}

	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "Invalid collector API key name", http.StatusBadRequest)
		return
	}

	if err := h.authService.DeleteCollectorAPIKey(r.Context(), name); err != nil {
		slog.Error("Failed to delete collector API key", "error", err)
		http.Error(w, "Failed to delete collector API key", http.StatusInternalServerError)
		return
	}

	htmx.Redirect(w, r, "/settings/")
}

// User API key handlers (LLM compatibility)

func (h *handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
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

func (h *handler) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
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
	if err := h.notifyService.Add(r.Context(), req); err != nil {
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

	if err := h.notifyService.Delete(r.Context(), user, id); err != nil {
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

	if err := h.notifyService.Toggle(r.Context(), user, id, setup.Enabled != 1); err != nil {
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

func (h *handler) getNotifierSubscriptions(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildNotifierSubscriptionsData(w, r)
	if !ok {
		return
	}
	h.t.RenderHTTP(w, r, "notifier-subscriptions", data)
}

func (h *handler) buildNotifierSubscriptionsData(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
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

	subscriptions, err := h.notifyService.GetSubscriptions(r.Context(), user, id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get notifier subscriptions", http.StatusInternalServerError)
		return nil, false
	}

	// Build subscription map with "collector:app" keys
	subscriptionMap := make(map[string]bool)
	for _, sub := range subscriptions {
		key := appKey(sub.CollectorName, sub.AppName)
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
		userApps, err := h.userService.FindAccessPolicy(r.Context(), user.ID)
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to get user apps", http.StatusInternalServerError)
			return nil, false
		}
		userAppSet := make(map[string]bool)
		for _, app := range userApps.Apps {
			userAppSet[appKey(app.CollectorName, app.AppName)] = true
		}

		// Filter collector apps to only show apps user has access to
		filtered := make(map[string][]string)
		for collector, apps := range collectorApps {
			var allowedApps []string
			for _, app := range apps {
				if userAppSet[appKey(collector, app)] {
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

func (h *handler) updateNotifierSubscriptions(w http.ResponseWriter, r *http.Request) {
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

	var subscriptions []notify.Subscription

	if allApps && user.IsAdmin {
		// Global wildcard subscription (all apps from all collectors)
		subscriptions = append(subscriptions, notify.Subscription{
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
			subscriptions = append(subscriptions, notify.Subscription{
				CollectorName: parts[0],
				AppName:       parts[1],
			})
		}

		// For non-admins: filter to only apps they have access to
		if !user.IsAdmin {
			userApps, err := h.userService.FindAccessPolicy(r.Context(), user.ID)
			if err != nil {
				http.Error(w, "Failed to get user apps", http.StatusInternalServerError)
				return
			}
			userAppSet := make(map[string]bool)
			for _, app := range userApps.Apps {
				userAppSet[appKey(app.CollectorName, app.AppName)] = true
			}

			var filtered []notify.Subscription
			for _, sub := range subscriptions {
				// Allow wildcards or check app access
				if sub.AppName == "*" || userAppSet[appKey(sub.CollectorName, sub.AppName)] {
					filtered = append(filtered, sub)
				}
			}
			subscriptions = filtered
		}
	}

	// Update subscriptions through service (simply writes to DB)
	if err := h.notifyService.SetSubscriptions(r.Context(), user, id, subscriptions); err != nil {
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
