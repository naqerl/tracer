package settings

import (
	"context"
	"embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/ai-shift/tracer/core/services/auth"
	"github.com/ai-shift/tracer/core/services/dbmanager"
	"github.com/ai-shift/tracer/core/services/notify"
	"github.com/ai-shift/tracer/db"
	"github.com/ai-shift/tracer/web/htmx"
	"github.com/ai-shift/tracer/web/middleware"
	"github.com/ai-shift/tracer/web/templates"
)

//go:embed views/*.html
var views embed.FS

type handler struct {
	t             templates.Templates
	authService   *auth.Service
	dbManager     *dbmanager.Manager
	notifyService *notify.Service
}

func InitMux(authService *auth.Service, dbManager *dbmanager.Manager, notifyService *notify.Service) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		t:             templates.New(views),
		authService:   authService,
		dbManager:     dbManager,
		notifyService: notifyService,
	}

	// Users management
	m.HandleFunc("GET /", h.settingsPage)
	m.HandleFunc("GET /users", h.listUsers)
	m.HandleFunc("POST /users", h.createUser)
	m.HandleFunc("POST /users/{id}/delete", h.deleteUser)
	m.HandleFunc("GET /users/{id}/apps", h.getUserApps)
	m.HandleFunc("POST /users/{id}/apps", h.updateUserApps)

	// API key
	m.HandleFunc("GET /api-key", h.getAPIKey)
	m.HandleFunc("POST /api-key/regenerate", h.regenerateAPIKey)

	// Notifiers management
	m.HandleFunc("GET /notifiers", h.listNotifiers)
	m.HandleFunc("POST /notifiers", h.createNotifier)
	m.HandleFunc("GET /notifiers/{id}", h.getNotifier)
	m.HandleFunc("POST /notifiers/{id}", h.updateNotifier)
	m.HandleFunc("POST /notifiers/{id}/delete", h.deleteNotifier)
	m.HandleFunc("POST /notifiers/{id}/toggle", h.toggleNotifier)
	m.HandleFunc("GET /notifiers/{id}/apps", h.getNotifierApps)
	m.HandleFunc("POST /notifiers/{id}/apps", h.updateNotifierApps)

	return m
}

func (h *handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	isAdmin := user.IsAdmin == 1

	data := map[string]any{
		"Title":       "Settings - Tracer",
		"CurrentUser": user,
		"IsAdmin":     isAdmin,
		"Nav":         templates.NewNavData("Settings", "settings", user.Username),
	}

	// Admin-only data
	if isAdmin {
		users, err := h.authService.ListUsers(r.Context())
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to list users", http.StatusInternalServerError)
			return
		}
		data["Users"] = users

		apiKey, _ := h.authService.GetAPIKey(r.Context())
		data["APIKey"] = apiKey

		// Admins see all notifiers
		notifiers, err := h.authService.ListNotifiers(r.Context())
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to list notifiers", http.StatusInternalServerError)
			return
		}
		data["Notifiers"] = notifiers
	} else {
		// Non-admins only see their own notifiers
		notifiers, err := h.authService.ListNotifiersByUser(r.Context(), user.ID)
		if err != nil {
			h.t.RenderError(w, r, "Error", "Failed to list notifiers", http.StatusInternalServerError)
			return
		}
		data["Notifiers"] = notifiers
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "settings-content", data)
	} else {
		h.t.RenderHTTP(w, r, "settings", data)
	}
}

// User handlers

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

	data := map[string]any{
		"Users":       users,
		"CurrentUser": user,
	}

	h.t.RenderHTTP(w, r, "users-list", data)
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

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
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

	if id == currentUser.ID {
		http.Error(w, "Cannot delete yourself", http.StatusBadRequest)
		return
	}

	if err := h.authService.DeleteUser(r.Context(), id); err != nil {
		slog.Error("Failed to delete user", "error", err)
		http.Error(w, "Failed to delete user", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) getUserApps(w http.ResponseWriter, r *http.Request) {
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

	selectedApps := r.Form["apps"]
	currentApps, err := h.authService.GetUserApps(r.Context(), id)
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
			if err := h.authService.AddUserApp(r.Context(), id, app); err != nil {
				slog.Error("Failed to add user app", "user_id", id, "app", app, "error", err)
			}
		}
	}

	for _, app := range currentApps {
		if !selectedMap[app] {
			if err := h.authService.RemoveUserApp(r.Context(), id, app); err != nil {
				slog.Error("Failed to remove user app", "user_id", id, "app", app, "error", err)
			}
		}
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

// API key handlers

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
	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

// Notifier handlers

type TelegramConfig struct {
	Token  string `json:"token"`
	ChatID int64  `json:"chat_id"`
}

func (h *handler) listNotifiers(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	var notifiers []db.Notifier
	var err error
	if user.IsAdmin == 1 {
		notifiers, err = h.authService.ListNotifiers(r.Context())
	} else {
		notifiers, err = h.authService.ListNotifiersByUser(r.Context(), user.ID)
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
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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
	if err := h.notifyService.AddNotifier(r.Context(), req, h.authService); err != nil {
		slog.Error("Failed to add notifier", "error", err)
		http.Error(w, "Failed to create notifier", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) getNotifier(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return
	}

	notifier, err := h.authService.GetNotifierDB(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Notifier not found", http.StatusNotFound)
		return
	}

	// Check ownership (admins can access any, others only their own)
	if user.IsAdmin != 1 && notifier.UserID != user.ID {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this notifier", http.StatusForbidden)
		return
	}

	apps, err := h.authService.GetNotifierApps(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get notifier apps", http.StatusInternalServerError)
		return
	}

	// Get available apps based on user access
	allApps, err := h.getAvailableApps(r.Context(), user)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list apps", http.StatusInternalServerError)
		return
	}

	notifierAppMap := make(map[string]bool)
	for _, app := range apps {
		notifierAppMap[app] = true
	}

	data := map[string]any{
		"Title":        "Edit Notifier - Tracer",
		"Notifier":     notifier,
		"NotifierApps": notifierAppMap,
		"AllApps":      allApps,
		"IsAdmin":      user.IsAdmin == 1,
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "notifier-edit-content", data)
	} else {
		h.t.RenderHTTP(w, r, "notifier-edit", data)
	}
}

func (h *handler) updateNotifier(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return
	}

	notifier, err := h.authService.GetNotifierDB(r.Context(), id)
	if err != nil {
		http.Error(w, "Notifier not found", http.StatusNotFound)
		return
	}

	// Check ownership
	if user.IsAdmin != 1 && notifier.UserID != user.ID {
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

		cfg := TelegramConfig{Token: token, ChatID: chatID}
		cfgBytes, _ := json.Marshal(cfg)
		configJSON = string(cfgBytes)
	default:
		configJSON = notifier.Config
	}

	enabled := notifier.Enabled == 1

	if err := h.authService.UpdateNotifier(r.Context(), id, name, notifier.Type, configJSON, enabled); err != nil {
		slog.Error("Failed to update notifier", "error", err)
		http.Error(w, "Failed to update notifier", http.StatusInternalServerError)
		return
	}

	// Re-register the notifier
	h.registerNotifierWithUser(r.Context(), id, user)

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) deleteNotifier(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return
	}

	notifier, err := h.authService.GetNotifierDB(r.Context(), id)
	if err != nil {
		http.Error(w, "Notifier not found", http.StatusNotFound)
		return
	}

	// Check ownership
	if user.IsAdmin != 1 && notifier.UserID != user.ID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := h.authService.DeleteNotifier(r.Context(), id); err != nil {
		slog.Error("Failed to delete notifier", "error", err)
		http.Error(w, "Failed to delete notifier", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) toggleNotifier(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return
	}

	notifier, err := h.authService.GetNotifierDB(r.Context(), id)
	if err != nil {
		http.Error(w, "Notifier not found", http.StatusNotFound)
		return
	}

	// Check ownership
	if user.IsAdmin != 1 && notifier.UserID != user.ID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if notifier.Enabled == 1 {
		err = h.authService.DisableNotifier(r.Context(), id)
	} else {
		err = h.authService.EnableNotifier(r.Context(), id)
	}

	if err != nil {
		slog.Error("Failed to toggle notifier", "error", err)
		http.Error(w, "Failed to toggle notifier", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

func (h *handler) getNotifierApps(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return
	}

	notifier, err := h.authService.GetNotifierDB(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Notifier not found", http.StatusNotFound)
		return
	}

	// Check ownership
	if user.IsAdmin != 1 && notifier.UserID != user.ID {
		h.t.RenderError(w, r, "Forbidden", "You don't have access to this notifier", http.StatusForbidden)
		return
	}

	apps, err := h.authService.GetNotifierApps(r.Context(), id)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to get notifier apps", http.StatusInternalServerError)
		return
	}

	// Get available apps based on user access
	allApps, err := h.getAvailableApps(r.Context(), user)
	if err != nil {
		h.t.RenderError(w, r, "Error", "Failed to list apps", http.StatusInternalServerError)
		return
	}

	notifierAppMap := make(map[string]bool)
	for _, app := range apps {
		notifierAppMap[app] = true
	}

	data := map[string]any{
		"Title":        "Edit Notifier Apps - Tracer",
		"Notifier":     notifier,
		"NotifierApps": notifierAppMap,
		"AllApps":      allApps,
		"IsAdmin":      user.IsAdmin == 1,
		"Nav":          templates.NewNavData("Configure Notifier Apps", "settings", user.Username),
	}

	if htmx.IsAjax(r) {
		h.t.RenderHTTP(w, r, "notifier-apps-content", data)
	} else {
		h.t.RenderHTTP(w, r, "notifier-apps", data)
	}
}

func (h *handler) updateNotifierApps(w http.ResponseWriter, r *http.Request) {
	user := middleware.GetUser(r)
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return
	}

	notifier, err := h.authService.GetNotifierDB(r.Context(), id)
	if err != nil {
		http.Error(w, "Notifier not found", http.StatusNotFound)
		return
	}

	// Check ownership
	if user.IsAdmin != 1 && notifier.UserID != user.ID {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	selectedApps := r.Form["apps"]
	allApps := r.FormValue("all_apps") == "on"

	// Only admins can select "all apps"
	if allApps && user.IsAdmin == 1 {
		selectedApps = []string{"*"}
	} else if user.IsAdmin != 1 {
		// Non-admins can only select from their allowed apps
		userApps, err := h.authService.GetUserApps(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "Failed to get user apps", http.StatusInternalServerError)
			return
		}
		userAppMap := make(map[string]bool)
		for _, app := range userApps {
			userAppMap[app] = true
		}
		// Filter selected apps to only those the user has access to
		var filteredApps []string
		for _, app := range selectedApps {
			if userAppMap[app] {
				filteredApps = append(filteredApps, app)
			}
		}
		selectedApps = filteredApps
	}

	// Update apps through service (handles DB, subscription notifications, re-registration)
	if err := h.notifyService.UpdateNotifierApps(r.Context(), id, user.ID, selectedApps, h.authService); err != nil {
		slog.Error("Failed to update notifier apps", "error", err)
		http.Error(w, "Failed to update notifier apps", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/settings/", http.StatusSeeOther)
}

// getAvailableApps returns apps available to a user (all apps for admins, user's apps for others)
func (h *handler) getAvailableApps(ctx context.Context, user *db.User) ([]string, error) {
	allApps, err := h.dbManager.ListApps()
	if err != nil {
		return nil, err
	}

	if user.IsAdmin == 1 {
		return allApps, nil
	}

	// Non-admins only see their allowed apps
	userApps, err := h.authService.GetUserApps(ctx, user.ID)
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

// registerNotifierWithUser creates and registers a notifier with the notification service
func (h *handler) registerNotifierWithUser(ctx context.Context, notifierID int64, user *db.User) {
	notifier, err := h.authService.GetNotifierDB(ctx, notifierID)
	if err != nil {
		slog.Error("Failed to get notifier for registration", "error", err)
		return
	}

	if notifier.Enabled != 1 {
		return
	}

	apps, err := h.authService.GetNotifierApps(ctx, notifierID)
	if err != nil {
		slog.Error("Failed to get notifier apps", "error", err)
		return
	}

	// For non-admins, filter apps to only those they have access to
	if user != nil && user.IsAdmin != 1 {
		userApps, err := h.authService.GetUserApps(ctx, user.ID)
		if err != nil {
			slog.Error("Failed to get user apps", "error", err)
			return
		}
		userAppMap := make(map[string]bool)
		for _, app := range userApps {
			userAppMap[app] = true
		}
		var filtered []string
		for _, app := range apps {
			if app == "*" {
				// Non-admins can't use wildcard, skip
				continue
			}
			if userAppMap[app] {
				filtered = append(filtered, app)
			}
		}
		apps = filtered
	}

	if len(apps) == 0 {
		return
	}

	var n notify.Notifier
	switch notifier.Type {
	case "telegram":
		var cfg TelegramConfig
		if err := json.Unmarshal([]byte(notifier.Config), &cfg); err != nil {
			slog.Error("Failed to parse telegram config", "error", err)
			return
		}
		tg, err := notify.NewTelegramNotifier(notifier.Name, cfg.Token, cfg.ChatID)
		if err != nil {
			slog.Error("Failed to create telegram notifier", "error", err)
			return
		}
		n = tg
	default:
		slog.Error("Unknown notifier type", "type", notifier.Type)
		return
	}

	// Register for each app
	for _, app := range apps {
		h.notifyService.RegisterNotifier(app, n)
	}
}
