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
	"slices"
	"strconv"
	"strings"

	panelbuild "tracer/apps/panel"
	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
	"tracer/apps/panel/internal/services/auth"
	"tracer/apps/panel/internal/services/ingest"
	"tracer/apps/panel/internal/services/notify"
	"tracer/apps/panel/internal/services/notify/forgejo"
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
	readOnly      bool
}

func appKey(collectorName, appName string) string {
	return collectorName + ":" + appName
}

// defaultForgejoBaseURL pre-fills the Forgejo base URL in the notifier form.
const defaultForgejoBaseURL = "https://git.naqerl.com"

func InitMux(authService *auth.Service, userService *usersvc.Service, ingestService *ingest.Service, dbManager *dbmanager.Manager, notifyService *notify.Service, cfg *config.Config, readOnly bool) *http.ServeMux {
	m := http.NewServeMux()
	h := handler{
		authService:   authService,
		userService:   userService,
		ingestService: ingestService,
		dbManager:     dbManager,
		notifyService: notifyService,
		cfg:           cfg,
		readOnly:      readOnly,
	}

	// Users management
	m.HandleFunc("GET /", h.settingsPage)
	m.HandleFunc("POST /users", h.createUser)
	m.HandleFunc("DELETE /users/{id}", h.deleteUser)
	m.HandleFunc("GET /users/{id}", h.getUser)
	m.HandleFunc("PUT /users/{id}/apps", h.updateUserApps)

	// Collector API keys
	m.HandleFunc("POST /admin/collector-api-keys", h.createCollectorAPIKey)
	m.HandleFunc("GET /admin/collector-api-keys/{id}", h.getCollectorAPIKey)
	m.HandleFunc("POST /admin/collector-api-keys/{id}/reveal", h.revealCollectorAPIKey)
	m.HandleFunc("DELETE /admin/collector-api-keys/{id}", h.deleteCollectorAPIKey)

	// User API keys (LLM compatibility)
	m.HandleFunc("POST /api-keys", h.createAPIKey)
	m.HandleFunc("GET /api-keys/{id}", h.getAPIKey)
	m.HandleFunc("POST /api-keys/{id}/reveal", h.revealAPIKey)
	m.HandleFunc("DELETE /api-keys/{id}", h.deleteAPIKey)

	// Notifiers management
	m.HandleFunc("GET /notifiers/new", h.notifierForm)
	m.HandleFunc("POST /notifiers/forgejo/lookup", h.forgejoLookup)
	m.HandleFunc("POST /notifiers/forgejo/options", h.forgejoOptions)
	m.HandleFunc("GET /notifiers/{id}", h.getNotifier)
	m.HandleFunc("POST /notifiers", h.createNotifier)
	m.HandleFunc("DELETE /notifiers/{id}", h.deleteNotifier)
	m.HandleFunc("PUT /notifiers/{id}/toggle", h.toggleNotifier)
	m.HandleFunc("PUT /notifiers/{id}/subscriptions", h.updateNotifierSubscriptions)

	// Config management
	m.HandleFunc("POST /config", h.updateConfig)

	return m
}

func (h *handler) settingsPage(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}

	props := view.SettingsPageProps{
		Title:       "Settings - Tracer",
		Version:     panelbuild.Version,
		CurrentUser: user,
		IsAdmin:     user.Role == domain.RoleAdmin,
		NotifierForm: view.NotifierFormView{
			ActiveType:     "ntfy",
			NtfyURL:        "https://ntfy.sh",
			ForgejoBaseURL: defaultForgejoBaseURL,
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
	props.Notifiers = notifiers

	// Load user's API keys (all users can have API keys)
	userAPIKeys, err := h.authService.ListUserAPIKeys(r.Context(), user, user.ID)
	if err != nil {
		slog.Error("Failed to list user API keys", "error", err)
	} else {
		for _, k := range userAPIKeys {
			props.UserAPIKeys = append(props.UserAPIKeys, view.UserAPIKeyView{
				ID:            k.ID,
				Name:          k.Name,
				LastUsed:      k.LastUsedAt,
				LastUsedValid: k.LastUsedSet,
			})
		}
	}

	// Admin-only data
	if user.Role == domain.RoleAdmin {
		users, err := h.userService.List(r.Context(), user)
		if err != nil {
			slog.Error("Failed to list users", "error", err)
			templates.ErrorPage("Error", "Failed to list users", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
			return
		}
		props.Users = append(props.Users, users...)

		collectorAPIKeys, err := h.authService.ListCollectorAPIKeys(r.Context(), user)
		if err != nil {
			slog.Error("Failed to list collector API keys", "error", err)
			templates.ErrorPage("Error", "Failed to list collector API keys", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
			return
		}
		for _, k := range collectorAPIKeys {
			props.CollectorAPIKeys = append(props.CollectorAPIKeys, view.CollectorAPIKeyView{
				ID:            k.ID,
				Name:          k.Name,
				CreatedAt:     k.CreatedAt,
				LastUsed:      k.LastUsedAt,
				LastUsedValid: k.LastUsedSet,
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
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}
	if user.Role != domain.RoleAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
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
	created, err := h.authService.Register(r.Context(), req)
	if err != nil {
		slog.Error("Failed to create user", "error", err)
		http.Error(w, "Failed to create user", http.StatusInternalServerError)
		return
	}

	// Navigate to the new user's detail page, where app permissions are managed.
	htmx.Redirect(w, r, fmt.Sprintf("/settings/users/%d", created.ID))
}

func (h *handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	currentUser, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}
	if currentUser.Role != domain.RoleAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
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

func (h *handler) getUser(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}
	if user.Role != domain.RoleAdmin {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	props, ok := h.buildUserDetailProps(user, id, w, r)
	if !ok {
		return
	}

	view.UserDetail(props).Render(r.Context(), w)
}

// buildUserDetailProps loads the user and their per-app grants, and renders an
// error page if anything fails. It returns ok=false when an error page has
// been written.
func (h *handler) buildUserDetailProps(user domain.User, id int64, w http.ResponseWriter, r *http.Request) (view.UserAppsProps, bool) {
	target, err := h.userService.FindByID(r.Context(), user, id)
	switch {
	case errors.Is(err, usersvc.ErrNotFound):
		templates.ErrorPage("Error", "User not found", http.StatusNotFound, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return view.UserAppsProps{}, false
	case err != nil:
		slog.Error("Failed to get user", "id", id, "error", err)
		templates.ErrorPage("Error", "Failed to get user", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return view.UserAppsProps{}, false
	}

	userApps, err := h.userService.FindAccessPolicy(r.Context(), id)
	if err != nil {
		templates.ErrorPage("Error", "Failed to get user apps", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return view.UserAppsProps{}, false
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
		return view.UserAppsProps{}, false
	}

	// Build view props.
	apps := make([]domain.ScopeEntry, 0)
	for collector, appNames := range collectorApps {
		for _, appName := range appNames {
			apps = append(apps, domain.ScopeEntry{
				Collector: collector,
				App:       appName,
				Granted:   userAppMap[appKey(collector, appName)],
			})
		}
	}

	return view.UserAppsProps{
		UserID:   target.ID,
		UserName: target.Username,
		Role:     target.Role,
		Apps:     apps,
		ReadOnly: h.readOnly,
	}, true
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
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}
	if user.Role != domain.RoleAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
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
			http.Error(w, "Failed to update user apps", http.StatusInternalServerError)
			return
		}
	}

	for _, app := range currentApps.Apps {
		pair := appKey(app.CollectorName, app.AppName)
		if !selectedMap[pair] {
			if err := h.userService.RevokeAccess(r.Context(), id, usersvc.AppAccess{CollectorName: app.CollectorName, AppName: app.AppName}); err != nil {
				slog.Error("Failed to remove user app", "user_id", id, "collector", app.CollectorName, "app", app.AppName, "error", err)
				http.Error(w, "Failed to update user apps", http.StatusInternalServerError)
				return
			}
		}
	}

	htmx.Redirect(w, r, fmt.Sprintf("/settings/users/%d", id))
}

// Collector API key handlers

func (h *handler) createCollectorAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}

	req := auth.GenerateCollectorAPIKeyRequest{
		Name: strings.TrimSpace(r.FormValue("name")),
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	created, err := h.authService.GenerateCollectorAPIKey(r.Context(), user, req)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrAccessDenied):
			http.Error(w, "Forbidden", http.StatusForbidden)
		default:
			slog.Error("Failed to generate collector API key", "error", err)
			http.Error(w, "Failed to generate collector API key", http.StatusInternalServerError)
		}
		return
	}

	// Navigate to the new key's detail page, where the plaintext can be revealed.
	htmx.Redirect(w, r, fmt.Sprintf("/settings/admin/collector-api-keys/%d", created.Key.ID))
}

func (h *handler) deleteCollectorAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid collector API key id", http.StatusBadRequest)
		return
	}

	if err := h.authService.DeleteCollectorAPIKey(r.Context(), user, id); err != nil {
		switch {
		case errors.Is(err, auth.ErrAccessDenied):
			http.Error(w, "Forbidden", http.StatusForbidden)
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "Collector API key not found", http.StatusNotFound)
		default:
			slog.Error("Failed to delete collector API key", "id", id, "error", err)
			http.Error(w, "Failed to delete collector API key", http.StatusInternalServerError)
		}
		return
	}

	htmx.Redirect(w, r, "/settings/")
}

func (h *handler) getCollectorAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid collector API key id", http.StatusBadRequest)
		return
	}

	key, err := h.authService.GetCollectorAPIKeyByID(r.Context(), user, id)
	// Access denied reads as not found so key existence is not revealed.
	switch {
	case errors.Is(err, auth.ErrAccessDenied), errors.Is(err, sql.ErrNoRows):
		templates.RenderError(w, r, "Error", "Collector API key not found", http.StatusNotFound)
		return
	case err != nil:
		slog.Error("Failed to get collector API key", "id", id, "error", err)
		templates.RenderError(w, r, "Error", "Failed to get collector API key", http.StatusInternalServerError)
		return
	}

	view.CollectorAPIKeyDetail(view.CollectorAPIKeyDetailProps{
		Title:         "Collector API Key - Tracer",
		KeyID:         key.ID,
		KeyName:       key.Name,
		CreatedAt:     key.CreatedAt,
		LastUsed:      key.LastUsedAt,
		LastUsedValid: key.LastUsedSet,
		CanReveal:     key.CanReveal,
		ReadOnly:      h.readOnly,
	}).Render(r.Context(), w)
}

// revealCollectorAPIKey toggles the plaintext visibility of a collector API key.
// The key is decrypted server-side and rendered as a fragment that htmx swaps
// into the detail page. It is registered as POST so the global read-only
// middleware (which rejects every non-GET request) makes reveal impossible in
// read-only mode.
func (h *handler) revealCollectorAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid collector API key id", http.StatusBadRequest)
		return
	}

	key, err := h.authService.GetCollectorAPIKeyByID(r.Context(), user, id)
	switch {
	case errors.Is(err, auth.ErrAccessDenied):
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	case errors.Is(err, sql.ErrNoRows):
		templates.RenderError(w, r, "Error", "Collector API key not found", http.StatusNotFound)
		return
	case err != nil:
		slog.Error("Failed to get collector API key", "id", id, "error", err)
		templates.RenderError(w, r, "Error", "Failed to get collector API key", http.StatusInternalServerError)
		return
	}

	props := view.APIKeyValueProps{
		RevealURL: "/settings/admin/collector-api-keys/" + strconv.FormatInt(id, 10) + "/reveal",
		TargetID:  "collector-api-key-value",
		CanReveal: key.CanReveal,
		Revealed:  r.FormValue("revealed") == "1",
		ReadOnly:  h.readOnly,
	}

	if props.Revealed {
		plaintext, err := h.authService.RevealCollectorAPIKey(r.Context(), user, key.ID)
		switch {
		case errors.Is(err, auth.ErrAccessDenied):
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		case errors.Is(err, auth.ErrKeyNotRevealable):
			props.RevealError = "This key was created before reveal support and cannot be shown again."
		case errors.Is(err, auth.ErrKeyDecryptFailed):
			props.RevealError = "Could not decrypt the key. Check that the key secret is unchanged."
		case err != nil:
			slog.Error("Failed to reveal collector API key", "id", id, "error", err)
			props.RevealError = "Failed to reveal the API key."
		default:
			props.PlaintextKey = plaintext
		}
	}

	// Never let a browser cache the plaintext.
	w.Header().Set("Cache-Control", "no-store")
	view.APIKeyValue(props).Render(r.Context(), w)
}

// User API key handlers (LLM compatibility)

func (h *handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}
	userID := user.ID

	req := auth.GenerateUserAPIKeyRequest{
		UserID: userID,
		Name:   strings.TrimSpace(r.FormValue("name")),
	}
	if err := req.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	created, err := h.authService.GenerateUserAPIKey(r.Context(), user, req)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrAccessDenied):
			http.Error(w, "Forbidden", http.StatusForbidden)
		default:
			slog.Error("Failed to generate user API key", "error", err)
			http.Error(w, "Failed to generate API key", http.StatusInternalServerError)
		}
		return
	}

	// Navigate to the new key's detail page, where the plaintext can be revealed.
	htmx.Redirect(w, r, fmt.Sprintf("/settings/api-keys/%d", created.Key.ID))
}

func (h *handler) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}

	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid API key ID", http.StatusBadRequest)
		return
	}

	if err := h.authService.DeleteUserAPIKey(r.Context(), user, id); err != nil {
		switch {
		case errors.Is(err, auth.ErrAccessDenied):
			http.Error(w, "Forbidden", http.StatusForbidden)
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "API key not found", http.StatusNotFound)
		default:
			slog.Error("Failed to delete user API key", "id", id, "error", err)
			http.Error(w, "Failed to delete API key", http.StatusInternalServerError)
		}
		return
	}

	htmx.Redirect(w, r, "/settings/")
}

// getAPIKey renders the user API key detail page, mirroring the collector API
// key detail page: the key is shown masked and can be revealed in place, and
// deletion happens on this page rather than in the settings table.
func (h *handler) getAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid API key ID", http.StatusBadRequest)
		return
	}

	key, err := h.authService.GetUserAPIKeyByID(r.Context(), user, id)
	// Access denied reads as not found so key existence is not revealed to
	// non-owners (key IDs are sequential).
	switch {
	case errors.Is(err, auth.ErrAccessDenied), errors.Is(err, sql.ErrNoRows):
		templates.RenderError(w, r, "Error", "API key not found", http.StatusNotFound)
		return
	case err != nil:
		slog.Error("Failed to get user API key", "id", id, "error", err)
		templates.RenderError(w, r, "Error", "Failed to get API key", http.StatusInternalServerError)
		return
	}

	view.UserAPIKeyDetail(view.UserAPIKeyDetailProps{
		Title:         "API Key - Tracer",
		KeyID:         key.ID,
		KeyName:       key.Name,
		CreatedAt:     key.CreatedAt,
		LastUsed:      key.LastUsedAt,
		LastUsedValid: key.LastUsedSet,
		CanReveal:     key.CanReveal,
		ReadOnly:      h.readOnly,
	}).Render(r.Context(), w)
}

// revealAPIKey toggles the plaintext visibility of a user API key. The key is
// decrypted server-side and rendered as a fragment that htmx swaps into the
// detail page. It is registered as POST so the global read-only middleware
// (which rejects every non-GET request) makes reveal impossible in read-only
// mode.
func (h *handler) revealAPIKey(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid API key ID", http.StatusBadRequest)
		return
	}

	key, err := h.authService.GetUserAPIKeyByID(r.Context(), user, id)
	switch {
	case errors.Is(err, auth.ErrAccessDenied):
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	case errors.Is(err, sql.ErrNoRows):
		templates.RenderError(w, r, "Error", "API key not found", http.StatusNotFound)
		return
	case err != nil:
		slog.Error("Failed to get user API key", "id", id, "error", err)
		templates.RenderError(w, r, "Error", "Failed to get API key", http.StatusInternalServerError)
		return
	}

	props := view.APIKeyValueProps{
		RevealURL: "/settings/api-keys/" + strconv.FormatInt(id, 10) + "/reveal",
		TargetID:  "user-api-key-value",
		CanReveal: key.CanReveal,
		Revealed:  r.FormValue("revealed") == "1",
		ReadOnly:  h.readOnly,
	}

	if props.Revealed {
		plaintext, err := h.authService.RevealUserAPIKey(r.Context(), user, key.ID)
		switch {
		case errors.Is(err, auth.ErrAccessDenied):
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		case errors.Is(err, auth.ErrKeyNotRevealable):
			props.RevealError = "This key was created before reveal support and cannot be shown again."
		case errors.Is(err, auth.ErrKeyDecryptFailed):
			props.RevealError = "Could not decrypt the key. Check that the key secret is unchanged."
		case err != nil:
			slog.Error("Failed to reveal user API key", "id", id, "error", err)
			props.RevealError = "Failed to reveal the API key."
		default:
			props.PlaintextKey = plaintext
		}
	}

	// Never let a browser cache the plaintext.
	w.Header().Set("Cache-Control", "no-store")
	view.APIKeyValue(props).Render(r.Context(), w)
}

// Notifier handlers

func (h *handler) getNotifier(w http.ResponseWriter, r *http.Request) {
	data, ok := h.buildNotifierDetailData(w, r)
	if !ok {
		return
	}
	// The page shows notifier config; keep secrets out of caches and history.
	w.Header().Set("Cache-Control", "no-store")
	view.NotifierDetail(data).Render(r.Context(), w)
}

func (h *handler) buildNotifierDetailData(w http.ResponseWriter, r *http.Request) (view.NotifierDetailProps, bool) {
	user, err := authmw.RequireUser(w, r)
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
		case errors.Is(err, notify.ErrNotifierNotFound), errors.Is(err, notify.ErrAccessDenied):
			templates.ErrorPage("Error", "Notifier not found", http.StatusNotFound, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		default:
			templates.ErrorPage("Error", "Failed to get notifier", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		}
		return view.NotifierDetailProps{}, false
	}

	options, err := h.notifyService.GetSubscriptionOptions(r.Context(), user, id)
	if err != nil {
		slog.Error("Failed to get notifier subscriptions", "error", err)
		templates.ErrorPage("Error", "Failed to get notifier subscriptions", http.StatusInternalServerError, templates.NavData{Since: "24h"}).Render(r.Context(), w)
		return view.NotifierDetailProps{}, false
	}

	return view.NotifierDetailProps{
		Setup:    setup,
		Apps:     options,
		ReadOnly: h.readOnly,
	}, true
}

func (h *handler) notifierForm(w http.ResponseWriter, r *http.Request) {
	if _, err := authmw.RequireUser(w, r); err != nil {
		return
	}

	activeType := strings.TrimSpace(r.URL.Query().Get("type"))
	switch activeType {
	case "", "ntfy":
		activeType = "ntfy"
	case "telegram", "forgejo":
	default:
		activeType = "ntfy"
	}

	form := view.NotifierFormView{
		ActiveType:     activeType,
		NtfyURL:        "https://ntfy.sh",
		ForgejoBaseURL: defaultForgejoBaseURL,
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
	user, err := authmw.RequireUser(w, r)
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

		if err != nil {
			http.Error(w, "Telegram chat ID must be a valid integer", http.StatusBadRequest)
			return
		}
		if chatID == 0 {
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
	case "forgejo":
		creds, err := forgejoCredentialsFromRequest(r)
		if err != nil {
			switch {
			case errors.Is(err, errForgejoFieldsMissing):
				renderForgejoCreateFieldError(w, r, "forgejo-auth-status", "Forgejo base URL and token are required")
			default:
				renderForgejoCreateFieldError(w, r, "forgejo-auth-status", err.Error())
			}
			return
		}

		ownerRepoValue := strings.TrimSpace(r.FormValue("owner_repo"))
		ownerRepo, err := splitForgejoOwnerRepo(ownerRepoValue)
		if err != nil {
			switch {
			case errors.Is(err, errForgejoMalformedOwnerRepo):
				renderForgejoCreateFieldError(w, r, "forgejo-owner-repo-error", "enter a repository as owner/repo")
			default:
				renderForgejoCreateFieldError(w, r, "forgejo-owner-repo-error", err.Error())
			}
			return
		}

		// Only autocomplete options are valid: the token must be able to see
		// the chosen owner/repo among its fetched repository pairs.
		pairs, err := creds.client.ListOwnerRepos(r.Context())
		if err != nil {
			if errors.Is(err, forgejo.ErrUnauthorized) {
				renderForgejoCreateFieldError(w, r, "forgejo-auth-status", err.Error())
				return
			}
			slog.Error("Failed to fetch forgejo owner repos for validation", "error", err)
			http.Error(w, "Could not validate owner/repo", http.StatusInternalServerError)
			return
		}
		if !slices.Contains(pairs, ownerRepoValue) {
			slog.Warn("owner/repo not visible to the provided token", "owner_repo", ownerRepoValue)
			renderForgejoCreateFieldError(w, r, "forgejo-owner-repo-error", fmt.Sprintf("owner/repo %q does not exist", ownerRepoValue))
			return
		}

		// Parse comma-separated label names
		var labelNames []string
		if labelsStr := strings.TrimSpace(r.FormValue("labels")); labelsStr != "" {
			for _, name := range strings.Split(labelsStr, ",") {
				if name = strings.TrimSpace(name); name != "" {
					labelNames = append(labelNames, name)
				}
			}
		}
		milestoneName := strings.TrimSpace(r.FormValue("milestone_name"))

		var labels []int64
		if len(labelNames) > 0 {
			actualLabels, err := creds.client.ListLabels(r.Context(), ownerRepo.owner, ownerRepo.repo)
			if err != nil {
				slog.Error("Failed to fetch labels for validation", "error", err)
				http.Error(w, "Could not validate labels", http.StatusInternalServerError)
				return
			}
			ids := forgejoLabelIDs(actualLabels)
			for _, name := range labelNames {
				id, ok := ids[name]
				if !ok {
					slog.Warn("Label not found in repository", "label", name)
					renderForgejoCreateFieldError(w, r, "forgejo-labels-error", fmt.Sprintf("label %q does not exist", name))
					return
				}
				labels = append(labels, id)
			}
		}

		var milestone int64
		if milestoneName != "" {
			actualMilestones, err := creds.client.ListMilestones(r.Context(), ownerRepo.owner, ownerRepo.repo)
			if err != nil {
				slog.Error("Failed to fetch milestones for validation", "error", err)
				http.Error(w, "Could not validate milestone", http.StatusInternalServerError)
				return
			}
			id, ok := forgejoMilestoneIDs(actualMilestones)[milestoneName]
			if !ok {
				slog.Warn("Milestone not found in repository", "milestone", milestoneName)
				renderForgejoCreateFieldError(w, r, "forgejo-milestone-error", fmt.Sprintf("milestone %q does not exist", milestoneName))
				return
			}
			milestone = id
		}

		cfgBytes, err := json.Marshal(forgejo.Config{
			BaseURL:   creds.baseURL,
			Token:     creds.token,
			Owner:     ownerRepo.owner,
			Repo:      ownerRepo.repo,
			Labels:    labels,
			Milestone: milestone,
		})
		if err != nil {
			slog.Error("Failed to marshal forgejo config", "error", err)
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
	id, err := h.notifyService.Add(r.Context(), user, req)
	if err != nil {
		slog.Error("Failed to add notifier", "error", err)
		if errors.Is(err, notify.ErrWelcomeValidation) {
			renderForgejoCreateFieldError(w, r, "forgejo-auth-status", welcomeValidationMessage(err))
			return
		}
		http.Error(w, "Failed to create notifier", http.StatusInternalServerError)
		return
	}

	// Navigate to the new notifier's detail page, where subscriptions are managed.
	htmx.Redirect(w, r, fmt.Sprintf("/settings/notifiers/%d", id))
}

// forgejoLookup validates the token against the Forgejo API and loads every
// visible "owner/repo" pair into the merged autocomplete. The connection
// status is rendered into #forgejo-auth-status (out-of-band) while the owner
// box is swapped into #forgejo-lookup.
func (h *handler) forgejoLookup(w http.ResponseWriter, r *http.Request) {
	if _, err := authmw.RequireUser(w, r); err != nil {
		return
	}

	creds, err := forgejoCredentialsFromRequest(r)
	if err != nil {
		switch {
		case errors.Is(err, errForgejoFieldsMissing):
			renderForgejoLookupError(w, r, "")
		default:
			renderForgejoLookupError(w, r, err.Error())
		}
		return
	}

	pairs, err := creds.client.ListOwnerRepos(r.Context())
	if err != nil {
		renderForgejoLookupError(w, r, err.Error())
		return
	}

	templates.FieldStatus("forgejo-auth-status", fmt.Sprintf("authenticated (found %d repositories)", len(pairs)), true, true).Render(r.Context(), w)
	view.ForgejoLookup(view.ForgejoLookupProps{
		Loaded: true,
		OwnerRepoBox: view.ForgejoOwnerRepoBoxProps{
			Value:   strings.TrimSpace(r.FormValue("owner_repo")),
			Options: pairs,
		},
	}).Render(r.Context(), w)
}

// forgejoOptions validates the chosen owner/repo against the autocomplete
// options and loads its labels and milestones into the optional inputs.
func (h *handler) forgejoOptions(w http.ResponseWriter, r *http.Request) {
	if _, err := authmw.RequireUser(w, r); err != nil {
		return
	}

	ownerRepo := strings.TrimSpace(r.FormValue("owner_repo"))
	if ownerRepo == "" {
		view.ForgejoSelects(view.ForgejoSelectsProps{}).Render(r.Context(), w)
		return
	}

	creds, err := forgejoCredentialsFromRequest(r)
	if err != nil {
		switch {
		case errors.Is(err, errForgejoFieldsMissing):
			renderForgejoOptionsOwnerRepoError(w, r, "Forgejo base URL and token are required")
		default:
			renderForgejoOptionsOwnerRepoError(w, r, err.Error())
		}
		return
	}

	ownerRepoPair, err := splitForgejoOwnerRepo(ownerRepo)
	if err != nil {
		renderForgejoOptionsOwnerRepoError(w, r, err.Error())
		return
	}

	pairs, err := creds.client.ListOwnerRepos(r.Context())
	if err != nil {
		if errors.Is(err, forgejo.ErrUnauthorized) {
			renderForgejoOptionsOwnerRepoError(w, r, err.Error())
			return
		}
		slog.Error("Failed to fetch forgejo owner repos", "error", err)
		view.ForgejoSelects(view.ForgejoSelectsProps{Status: "could not load repositories: " + err.Error()}).Render(r.Context(), w)
		return
	}
	if !slices.Contains(pairs, ownerRepo) {
		renderForgejoOptionsOwnerRepoError(w, r, fmt.Sprintf("owner/repo %q does not exist", ownerRepo))
		return
	}

	labels, err := creds.client.ListLabels(r.Context(), ownerRepoPair.owner, ownerRepoPair.repo)
	if err != nil {
		slog.Error("Failed to list forgejo labels", "owner", ownerRepoPair.owner, "repo", ownerRepoPair.repo, "error", err)
		view.ForgejoSelects(view.ForgejoSelectsProps{Status: "could not load labels: " + err.Error()}).Render(r.Context(), w)
		return
	}
	milestones, err := creds.client.ListMilestones(r.Context(), ownerRepoPair.owner, ownerRepoPair.repo)
	if err != nil {
		slog.Error("Failed to list forgejo milestones", "owner", ownerRepoPair.owner, "repo", ownerRepoPair.repo, "error", err)
		view.ForgejoSelects(view.ForgejoSelectsProps{Status: "could not load milestones: " + err.Error()}).Render(r.Context(), w)
		return
	}

	labelOpts := make([]view.ForgejoOption, 0, len(labels))
	for _, l := range labels {
		labelOpts = append(labelOpts, view.ForgejoOption{ID: l.ID, Name: l.Name})
	}
	milestoneOpts := make([]view.ForgejoOption, 0, len(milestones))
	for _, m := range milestones {
		milestoneOpts = append(milestoneOpts, view.ForgejoOption{ID: m.ID, Name: m.Title})
	}

	view.ForgejoSelects(view.ForgejoSelectsProps{
		Labels:     labelOpts,
		Milestones: milestoneOpts,
		Loaded:     true,
	}).Render(r.Context(), w)
}

func (h *handler) deleteNotifier(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
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
	user, err := authmw.RequireUser(w, r)
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

func (h *handler) updateNotifierSubscriptions(w http.ResponseWriter, r *http.Request) {
	user, err := authmw.RequireUser(w, r)
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
	user, err := authmw.RequireUser(w, r)
	if err != nil {
		return
	}
	if user.Role != domain.RoleAdmin {
		http.Error(w, "Forbidden", http.StatusForbidden)
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
