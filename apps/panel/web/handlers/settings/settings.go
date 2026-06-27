package settings

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/notify"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/web/handlers/settings/view"
	"tracer/apps/panel/web/htmx"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/templates"
)

type handler struct {
	authService   *auth.Service
	userService   *usersvc.Service
	ingestService *ingest.Service
	dbManager     *dbmanager.Manager
	notifyService *notify.Service
	cfg           *config.Config
}

func appKey(collectorName, appName string) string {
	return collectorName + ":" + appName
}

func InitMux(authService *auth.Service, userService *usersvc.Service, ingestService *ingest.Service, dbManager *dbmanager.Manager, notifyService *notify.Service, cfg *config.Config) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		authService:   authService,
		userService:   userService,
		ingestService: ingestService,
		dbManager:     dbManager,
		notifyService: notifyService,
		cfg:           cfg,
	}

	// Users management
	m.HandleFunc("GET /", h.settingsPage)
	m.HandleFunc("POST /users", h.createUser)
	m.HandleFunc("DELETE /users/{id}", h.deleteUser)
	m.HandleFunc("GET /users/{id}", h.getUser)
	m.HandleFunc("GET /users/{id}/apps", h.getUserApps)
	m.HandleFunc("PUT /users/{id}/apps", h.updateUserApps)

	// Collector API keys
	m.HandleFunc("POST /admin/collector-api-keys", h.createCollectorAPIKey)
	m.HandleFunc("GET /admin/collector-api-keys/{name}", h.getCollectorAPIKey)
	m.HandleFunc("DELETE /admin/collector-api-keys/{name}", h.deleteCollectorAPIKey)

	// User API keys (LLM compatibility)
	m.HandleFunc("POST /api-keys", h.createAPIKey)
	m.HandleFunc("DELETE /api-keys/{id}", h.deleteAPIKey)

	// Notifiers management
	m.HandleFunc("GET /notifiers/new", h.notifierForm)
	m.HandleFunc("GET /notifiers/{id}", h.getNotifier)
	m.HandleFunc("POST /notifiers", h.createNotifier)
	m.HandleFunc("DELETE /notifiers/{id}", h.deleteNotifier)
	m.HandleFunc("PUT /notifiers/{id}/toggle", h.toggleNotifier)
	m.HandleFunc("GET /notifiers/{id}/subscriptions", h.getNotifierSubscriptions)
	m.HandleFunc("PUT /notifiers/{id}/subscriptions", h.updateNotifierSubscriptions)

	// Config management
	m.HandleFunc("POST /config", h.updateConfig)

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
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	props := view.SettingsPageProps{
		Title:       "Settings - Tracer",
		CurrentUser: view.CurrentUserView{Username: user.Username, IsAdmin: user.IsAdmin},
		IsAdmin:     user.IsAdmin,
		NotifierForm: view.NotifierFormView{
			ActiveType: "ntfy",
			NtfyURL:    "https://ntfy.sh",
		},
		CPUDeltaPercent: h.cfg.GetCPUDeltaPercent(),
		MemDeltaKB:      h.cfg.GetMemDeltaKB(),
		DiskDeltaKB:     h.cfg.GetDiskDeltaKB(),
	}

	// Load notifiers (admins see all, non-admins see only their own)
	notifiers, err := h.notifyService.List(r.Context(), user)
	if err != nil {
		slog.Error("Failed to list notifiers", "error", err)
		templates.ErrorPage("Error", "Failed to list notifiers", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return
	}
	for _, n := range notifiers {
		props.Notifiers = append(props.Notifiers, view.NotifierView{
			ID:      n.ID,
			Name:    n.Name,
			Type:    n.Type,
			Enabled: n.Enabled,
		})
	}

	// Load user's API keys (all users can have API keys)
	userAPIKeys, err := h.authService.ListUserAPIKeys(r.Context(), user.ID)
	if err != nil {
		slog.Error("Failed to list user API keys", "error", err)
	} else {
		for _, k := range userAPIKeys {
			props.UserAPIKeys = append(props.UserAPIKeys, view.UserAPIKeyView{
				ID:            k.ID,
				Name:          k.Name,
				KeySuffix:     k.KeySuffix,
				LastUsed:      k.LastUsedAt.Int64,
				LastUsedValid: k.LastUsedAt.Valid,
			})
		}
	}

	// Admin-only data
	if user.IsAdmin {
		users, err := h.userService.List(r.Context())
		if err != nil {
			slog.Error("Failed to list users", "error", err)
			templates.ErrorPage("Error", "Failed to list users", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
			return
		}
		for _, u := range users {
			props.Users = append(props.Users, view.UserView{
				ID:        u.ID,
				Username:  u.Username,
				IsAdmin:   u.IsAdmin == 1,
				CreatedAt: u.CreatedAt,
			})
		}

		collectorAPIKeys, err := h.authService.ListCollectorAPIKeys(r.Context())
		if err != nil {
			slog.Error("Failed to list collector API keys", "error", err)
			templates.ErrorPage("Error", "Failed to list collector API keys", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
			return
		}
		for _, k := range collectorAPIKeys {
			props.CollectorAPIKeys = append(props.CollectorAPIKeys, view.CollectorAPIKeyView{
				Name:          k.Name,
				KeySuffix:     k.KeySuffix,
				CreatedAt:     k.CreatedAt,
				LastUsed:      k.LastUsedAt.Int64,
				LastUsedValid: k.LastUsedAt.Valid,
			})
		}
	}

	// Generate default ntfy topic for form
	topic, err := generateNtfyTopic()
	if err != nil {
		slog.Error("Failed to generate ntfy topic", "error", err)
	} else {
		props.NotifierForm.NtfyTopic = topic
	}

	view.SettingsPage(props).Render(r.Context(), w)
}

func (h *handler) createUser(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdminUser(w, r); err != nil {
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	isAdmin := r.FormValue("is_admin") == "on"

	req := auth.RegisterRequest{
		Username: strings.TrimSpace(username),
		Password: password,
		IsAdmin:  isAdmin,
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, err := h.authService.Register(r.Context(), req)
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
	h.getUser(w, r)
}

func (h *handler) getUser(w http.ResponseWriter, r *http.Request) {
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

	user, err := h.userService.FindByID(r.Context(), id)
	if err != nil {
		templates.ErrorPage("Error", "User not found", http.StatusNotFound, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return
	}

	userApps, err := h.userService.FindAccessPolicy(r.Context(), id)
	if err != nil {
		templates.ErrorPage("Error", "Failed to get user apps", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return
	}

	// Build user apps map keyed by "collector:app".
	userAppMap := make(map[string]bool)
	for _, app := range userApps.Apps {
		userAppMap[appKey(app.CollectorName, app.AppName)] = true
	}

	// Get apps grouped by collector
	collectorApps, err := h.listAppsByCollector(r.Context())
	if err != nil {
		templates.ErrorPage("Error", "Failed to list apps", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return
	}

	// Build view props
	var apps []view.UserAppView
	for collector, appNames := range collectorApps {
		for _, appName := range appNames {
			apps = append(apps, view.UserAppView{
				Collector: collector,
				App:       appName,
				Granted:   userAppMap[appKey(collector, appName)],
			})
		}
	}

	view.UserApps(view.UserAppsProps{
		UserID:   user.ID,
		UserName: user.Username,
		Apps:     apps,
	}).Render(r.Context(), w)

	_ = currentUser
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

	htmx.Redirect(w, r, fmt.Sprintf("/settings/users/%d", id))
}

// Collector API key handlers

func (h *handler) createCollectorAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdminUser(w, r); err != nil {
		return
	}

	req := auth.GenerateCollectorAPIKeyRequest{
		Name: strings.TrimSpace(r.FormValue("name")),
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	_, _, err := h.authService.GenerateCollectorAPIKey(r.Context(), req)
	if err != nil {
		slog.Error("Failed to generate collector API key", "error", err)
		http.Error(w, "Failed to generate collector API key", http.StatusInternalServerError)
		return
	}

	// Redirect back to settings page
	htmx.Redirect(w, r, "/settings/")
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

func (h *handler) getCollectorAPIKey(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdminUser(w, r); err != nil {
		return
	}

	name := r.PathValue("name")
	if name == "" {
		http.Error(w, "Invalid collector API key name", http.StatusBadRequest)
		return
	}

	key, err := h.authService.GetCollectorAPIKeyByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			templates.ErrorPage("Error", "Collector API key not found", http.StatusNotFound, templates.NavData{Since: "24h"}).Render(r.Context(), w)
			return
		}
		slog.Error("Failed to get collector API key", "name", name, "error", err)
		templates.ErrorPage("Error", "Failed to get collector API key", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return
	}

	view.CollectorAPIKeyDetail(view.CollectorAPIKeyDetailProps{
		Title:         "Collector API Key - Tracer",
		KeyName:       key.Name,
		KeySuffix:     key.KeySuffix,
		CreatedAt:     key.CreatedAt,
		LastUsed:      key.LastUsedAt.Int64,
		LastUsedValid: key.LastUsedAt.Valid,
	}).Render(r.Context(), w)
}

// User API key handlers (LLM compatibility)

func (h *handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	req := auth.GenerateUserAPIKeyRequest{
		UserID: user.ID,
		Name:   strings.TrimSpace(r.FormValue("name")),
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	_, _, err = h.authService.GenerateUserAPIKey(r.Context(), req)
	if err != nil {
		slog.Error("Failed to generate user API key", "error", err)
		http.Error(w, "Failed to generate API key", http.StatusInternalServerError)
		return
	}

	// Redirect back
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

func (h *handler) getNotifier(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildNotifierDetailData(w, r)
	if !ok {
		return
	}
	view.NotifierDetail(data).Render(r.Context(), w)
}

func (h *handler) buildNotifierDetailData(w http.ResponseWriter, r *http.Request) (view.NotifierDetailProps, bool) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return view.NotifierDetailProps{}, false
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return view.NotifierDetailProps{}, false
	}

	setup, err := h.notifyService.GetSetup(r.Context(), user, id)
	if err != nil {
		switch {
		case errors.Is(err, notify.ErrNotifierNotFound):
			templates.ErrorPage("Error", "Notifier not found", http.StatusNotFound, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		case errors.Is(err, notify.ErrAccessDenied):
			templates.ErrorPage("Forbidden", "You don't have access to this notifier", http.StatusForbidden, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		default:
			templates.ErrorPage("Error", "Failed to get notifier", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		}
		return view.NotifierDetailProps{}, false
	}

	return view.NotifierDetailProps{
		Title:        "Notifier - Tracer",
		NotifierID:   setup.ID,
		NotifierName: setup.Name,
		NotifierType: setup.Type,
		Enabled:      setup.Enabled,
		Config:       setup.Config,
	}, true
}

func (h *handler) notifierForm(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAuthenticatedUser(w, r); err != nil {
		return
	}

	activeType := strings.TrimSpace(r.URL.Query().Get("type"))
	switch activeType {
	case "", "ntfy":
		activeType = "ntfy"
	case "telegram":
	default:
		activeType = "ntfy"
	}

	form := view.NotifierFormView{
		ActiveType: activeType,
		NtfyURL:    "https://ntfy.sh",
	}

	if activeType == "ntfy" {
		topic, err := generateNtfyTopic()
		if err != nil {
			slog.Error("Failed to generate ntfy topic", "error", err)
			http.Error(w, "Failed to render notifier form", http.StatusInternalServerError)
			return
		}
		form.NtfyTopic = topic
	}

	view.NotifierForm(form).Render(r.Context(), w)
}

func (h *handler) createNotifier(w http.ResponseWriter, r *http.Request) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
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
		chatIDStr := strings.TrimSpace(r.FormValue("telegram_chat_id"))
		chatID, err := strconv.ParseInt(chatIDStr, 10, 64)

		if token == "" || chatIDStr == "" {
			http.Error(w, "Telegram token and chat ID required", http.StatusBadRequest)
			return
		}

		if err != nil || chatID == 0 {
			http.Error(w, "Telegram chat ID must be a valid integer", http.StatusBadRequest)
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
	case "ntfy":
		url := strings.TrimRight(strings.TrimSpace(r.FormValue("ntfy_url")), "/")
		topic := strings.TrimSpace(r.FormValue("ntfy_topic"))

		if url == "" || topic == "" {
			http.Error(w, "ntfy URL and topic required", http.StatusBadRequest)
			return
		}

		cfg := struct {
			URL   string `json:"url"`
			Topic string `json:"topic"`
		}{
			URL:   url,
			Topic: topic,
		}
		cfgBytes, err := json.Marshal(cfg)
		if err != nil {
			slog.Error("Failed to marshal ntfy config", "error", err)
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
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
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

	redirectPath := "/settings/"
	if currentURL := r.Header.Get("HX-Current-URL"); currentURL != "" {
		if parsed, err := url.Parse(currentURL); err == nil && parsed.Path != "" {
			redirectPath = parsed.Path
		}
	}

	htmx.Redirect(w, r, redirectPath)
}

func (h *handler) getNotifierSubscriptions(w http.ResponseWriter, r *http.Request) {
	props, ok := h.buildNotifierSubscriptionsData(w, r)
	if !ok {
		return
	}
	view.NotifierSubscriptions(props).Render(r.Context(), w)
}

func (h *handler) buildNotifierSubscriptionsData(w http.ResponseWriter, r *http.Request) (view.NotifierSubscriptionsProps, bool) {
	user, err := h.requireAuthenticatedUser(w, r)
	if err != nil {
		return view.NotifierSubscriptionsProps{}, false
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid notifier ID", http.StatusBadRequest)
		return view.NotifierSubscriptionsProps{}, false
	}

	setup, err := h.notifyService.GetSetup(r.Context(), user, id)
	if err != nil {
		switch {
		case errors.Is(err, notify.ErrNotifierNotFound):
			templates.ErrorPage("Error", "Notifier not found", http.StatusNotFound, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		case errors.Is(err, notify.ErrAccessDenied):
			templates.ErrorPage("Forbidden", "You don't have access to this notifier", http.StatusForbidden, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		default:
			templates.ErrorPage("Error", "Failed to get notifier", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		}
		return view.NotifierSubscriptionsProps{}, false
	}

	subscriptions, err := h.notifyService.GetSubscriptions(r.Context(), user, id)
	if err != nil {
		templates.ErrorPage("Error", "Failed to get notifier subscriptions", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return view.NotifierSubscriptionsProps{}, false
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
		templates.ErrorPage("Error", "Failed to list collectors", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return view.NotifierSubscriptionsProps{}, false
	}

	// Build structured data: map[collector][]app
	collectorApps := make(map[string][]string)
	for _, collector := range collectors {
		apps, err := h.ingestService.ListApps(collector)
		if err != nil {
			slog.Error("Failed to list apps for collector", "collector", collector, "error", err)
			templates.ErrorPage("Error", "Failed to list apps", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
			return view.NotifierSubscriptionsProps{}, false
		}
		collectorApps[collector] = apps
	}

	// Filter by user access for non-admins
	if !user.IsAdmin {
		userApps, err := h.userService.FindAccessPolicy(r.Context(), user.ID)
		if err != nil {
			templates.ErrorPage("Error", "Failed to get user apps", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
			return view.NotifierSubscriptionsProps{}, false
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

	// Build app list for view
	var apps []view.NotifierAppView
	for collector, appNames := range collectorApps {
		for _, appName := range appNames {
			apps = append(apps, view.NotifierAppView{
				CollectorName: collector,
				AppName:       appName,
				Enabled:       subscriptionMap[appKey(collector, appName)],
			})
		}
	}

	props := view.NotifierSubscriptionsProps{
		Title:        "Edit Notifier Apps - Tracer",
		NotifierID:   id,
		NotifierName: setup.Name,
		Apps:         apps,
	}

	return props, true
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

	// Build list of subscriptions from form values
	selectedPairs := r.Form["apps"]

	var subscriptions []notify.Subscription
	for _, pair := range selectedPairs {
		parts := strings.Split(pair, ":")
		if len(parts) != 2 {
			continue
		}
		subscriptions = append(subscriptions, notify.Subscription{
			CollectorName: parts[0],
			AppName:       parts[1],
		})
	}

	// Update subscriptions through service (replaces all)
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

	htmx.Redirect(w, r, fmt.Sprintf("/settings/notifiers/%d", id))
}

func generateNtfyTopic() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate random bytes: %w", err)
	}
	hash := sha256.Sum256(buf)
	return hex.EncodeToString(hash[:8]), nil
}

func (h *handler) updateConfig(w http.ResponseWriter, r *http.Request) {
	if _, err := h.requireAdminUser(w, r); err != nil {
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	cpuDelta, err := strconv.ParseFloat(r.FormValue("cpu_delta_percent"), 64)
	if err != nil || cpuDelta < 0 {
		http.Error(w, "Invalid CPU delta percent", http.StatusBadRequest)
		return
	}

	memDelta, err := strconv.Atoi(r.FormValue("mem_delta_kb"))
	if err != nil || memDelta < 0 {
		http.Error(w, "Invalid memory delta KB", http.StatusBadRequest)
		return
	}

	diskDelta, err := strconv.Atoi(r.FormValue("disk_delta_kb"))
	if err != nil || diskDelta < 0 {
		http.Error(w, "Invalid disk delta KB", http.StatusBadRequest)
		return
	}

	h.cfg.SetCPUDeltaPercent(cpuDelta)
	h.cfg.SetMemDeltaKB(memDelta)
	h.cfg.SetDiskDeltaKB(diskDelta)

	htmx.Redirect(w, r, "/settings/")
}
