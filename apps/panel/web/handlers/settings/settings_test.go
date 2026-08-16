package settings

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	panelbuild "tracer/apps/panel"
	paneldb "tracer/apps/panel/db"
	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
	ingestsvc "tracer/apps/panel/internal/services/ingest"
	notifysvc "tracer/apps/panel/internal/services/notify"
	"tracer/apps/panel/internal/services/notify/forgejo"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/testutil"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/apps/panel/web/templates"
)

// newAuthedHandler builds the settings mux behind the auth middleware and
// registers a user (admin or not). It returns the handler, the auth service
// (to create API keys), the panel DB handle (for direct inserts) and a session
// token cookie.
func newAuthedHandler(t *testing.T, isAdmin, readOnly bool) (http.Handler, *authsvc.Service, *dbmanager.DBHandle, string) {
	h, a, d, tok, _ := newAuthedHandlerWithFactories(t, isAdmin, readOnly, map[string]notifysvc.FromRawJSON{})
	return h, a, d, tok
}

func newAuthedHandlerWithFactories(t *testing.T, isAdmin, readOnly bool, factories map[string]notifysvc.FromRawJSON) (http.Handler, *authsvc.Service, *dbmanager.DBHandle, string, *notifysvc.Service) {
	t.Helper()

	db := testutil.NewTestDB(t)
	// Low bcrypt cost: cost 12 is ~250ms per hash, too slow for the suite.
	authService := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)
	userService := usersvc.NewService(db)

	user, err := authService.Register(t.Context(), authsvc.RegisterRequest{
		Username: "alice",
		Password: "secret",
		IsAdmin:  isAdmin,
	})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	token, err := authService.CreateSession(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	dbMgr, err := dbmanager.New(t.TempDir())
	if err != nil {
		t.Fatalf("create db manager: %v", err)
	}
	t.Cleanup(func() {
		if err := dbMgr.Close(); err != nil {
			t.Errorf("close db manager: %v", err)
		}
	})

	cfg := config.New(config.Panel{
		Port:          "8080",
		DataDir:       t.TempDir(),
		Host:          "https://tracer.example.com",
		SampleRate:    0.1,
		RetentionDays: 90,
	}, t.TempDir()+"/config.yaml")

	ingestService := ingestsvc.New(dbMgr, make(chan notifysvc.Notification, 10), func(_, _ string, _ int64) string { return "" }, cfg)
	notifyService := notifysvc.New(db, userService, factories)

	mux := http.NewServeMux()
	mux.Handle("/settings/", http.StripPrefix("/settings", InitMux(authService, userService, ingestService, dbMgr, notifyService, cfg, readOnly)))
	handler := authmw.BuildAuthMiddleware(authService, userService)(mux)

	return handler, authService, db, token, notifyService
}

func revealRequest(id int64, token string, revealed bool) *http.Request {
	body := "revealed=0"
	if revealed {
		body = "revealed=1"
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/admin/collector-api-keys/"+strconv.FormatInt(id, 10)+"/reveal", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	return req
}

// collectorKeyID resolves the id of the collector API key with the given name
// through the public list API.
func collectorKeyID(t *testing.T, authService *authsvc.Service, name string) int64 {
	t.Helper()
	keys, err := authService.ListCollectorAPIKeys(t.Context(), domain.System{})
	if err != nil {
		t.Fatalf("list collector api keys: %v", err)
	}
	for _, k := range keys {
		if k.Name == name {
			return k.ID
		}
	}
	t.Fatalf("collector api key %q not found", name)
	return 0
}

func TestRevealCollectorAPIKeyReturnsPlaintextForAdmin(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, false)

	key, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest(collectorKeyID(t, authService, "prod-a"), token, true))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), key) {
		t.Fatalf("expected plaintext key in response, got %q", rr.Body.String())
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", rr.Header().Get("Cache-Control"))
	}
}

func TestRevealCollectorAPIKeyHidesPlaintextForAdmin(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, false)

	key, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest(collectorKeyID(t, authService, "prod-a"), token, false))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), key) {
		t.Fatal("expected plaintext NOT to be in the hidden fragment")
	}
	if !strings.Contains(rr.Body.String(), "\u2022") {
		t.Fatalf("expected masked placeholder in hidden fragment, got %q", rr.Body.String())
	}
}

func TestRevealCollectorAPIKeyForbiddenForNonAdmin(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, false, false)

	if _, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest(collectorKeyID(t, authService, "prod-a"), token, true))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin reveal, got %d", rr.Code)
	}
}

func TestRevealCollectorAPIKeyNotFound(t *testing.T) {
	handler, _, _, token := newAuthedHandler(t, true, false)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest(999999, token, true))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown key, got %d", rr.Code)
	}
}

func TestCollectorAPIKeyDetailPageDoesNotLeakPlaintext(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, false)

	key, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/"+strconv.FormatInt(collectorKeyID(t, authService, "prod-a"), 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), key) {
		t.Fatal("expected detail page GET to never contain the plaintext key")
	}
	if !strings.Contains(rr.Body.String(), "\u2022") {
		t.Fatalf("expected masked placeholder on detail page, got %q", rr.Body.String())
	}
}

func TestCollectorAPIKeyDetailPageReadOnlyHidesRevealButton(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, true)

	if _, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/"+strconv.FormatInt(collectorKeyID(t, authService, "prod-a"), 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "/reveal") {
		t.Fatal("expected no reveal button in read-only detail page")
	}
	if !strings.Contains(body, "Read-only") {
		t.Fatalf("expected read-only indicator, got %q", body)
	}
}

func TestRevealCollectorAPIKeyLegacyFragmentShowsNotRevealable(t *testing.T) {
	ctx := t.Context()
	handler, authService, db, token := newAuthedHandler(t, true, false)

	// Simulate a key created before reveal support: empty ciphertext.
	if err := db.Write(ctx, func(tx paneldb.DBTX) error {
		_, err := paneldb.New(tx).CreateCollectorAPIKey(ctx, paneldb.CreateCollectorAPIKeyParams{
			Name:      "legacy",
			KeyHash:   "deadbeef",
			CreatedAt: 1,
		})
		return err
	}); err != nil {
		t.Fatalf("insert legacy key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest(collectorKeyID(t, authService, "legacy"), token, true))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "\u2022") {
		t.Fatalf("expected masked placeholder in legacy fragment, got %q", body)
	}
	if !strings.Contains(body, "created before reveal support") {
		t.Fatalf("expected not-revealable message, got %q", body)
	}
}

func TestRevealCollectorAPIKeyDecryptFailureShowsError(t *testing.T) {
	ctx := t.Context()
	handler, authService, db, token := newAuthedHandler(t, true, false)

	// Insert a key with garbage ciphertext so decryption fails.
	if err := db.Write(ctx, func(tx paneldb.DBTX) error {
		_, err := paneldb.New(tx).CreateCollectorAPIKey(ctx, paneldb.CreateCollectorAPIKeyParams{
			Name:         "broken",
			KeyHash:      "deadbeef",
			KeyEncrypted: "not-valid-base64!!",
			CreatedAt:    1,
		})
		return err
	}); err != nil {
		t.Fatalf("insert broken key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest(collectorKeyID(t, authService, "broken"), token, true))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "\u2022") {
		t.Fatalf("expected masked placeholder in decrypt-failure fragment, got %q", body)
	}
	if !strings.Contains(body, "Could not decrypt the key") {
		t.Fatalf("expected decrypt error message, got %q", body)
	}
}

// userAPIKeyID returns the ID of the first user API key owned by the session
// user, creating it with the given name if none exists. It resolves the user
// ID from the session token so tests exercise the public service API.
func userAPIKeyID(t *testing.T, authService *authsvc.Service, token, name string) int64 {
	t.Helper()
	userID, _, err := authService.ValidateSession(t.Context(), token)
	if err != nil {
		t.Fatalf("resolve session user: %v", err)
	}
	keys, err := authService.ListUserAPIKeys(t.Context(), domain.User{ID: userID}, userID)
	if err != nil {
		t.Fatalf("list user api keys: %v", err)
	}
	for _, k := range keys {
		if k.Name == name {
			return k.ID
		}
	}
	if _, err := authService.GenerateUserAPIKey(t.Context(), domain.User{ID: userID}, authsvc.GenerateUserAPIKeyRequest{UserID: userID, Name: name}); err != nil {
		t.Fatalf("generate user api key: %v", err)
	}
	// The plaintext key is returned; resolve the row through the list API.
	keys, err = authService.ListUserAPIKeys(t.Context(), domain.User{ID: userID}, userID)
	if err != nil {
		t.Fatalf("list user api keys after generate: %v", err)
	}
	for _, k := range keys {
		if k.Name == name {
			return k.ID
		}
	}
	t.Fatalf("generated user api key %q not found", name)
	return 0
}

func userAPIKeyRevealRequest(id int64, token string, revealed bool) *http.Request {
	body := "revealed=0"
	if revealed {
		body = "revealed=1"
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/api-keys/"+strconv.FormatInt(id, 10)+"/reveal", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	return req
}

// revealAsAdmin reveals a user API key plaintext via the service as an admin
// actor, for comparing against HTTP responses.
func revealAsAdmin(t *testing.T, authService *authsvc.Service, token string, id int64) string {
	t.Helper()
	userID, _, err := authService.ValidateSession(t.Context(), token)
	if err != nil {
		t.Fatalf("resolve session user: %v", err)
	}
	key, err := authService.GetUserAPIKeyByID(t.Context(), domain.User{ID: userID, Role: domain.RoleAdmin}, id)
	if err != nil {
		t.Fatalf("get user api key: %v", err)
	}
	plaintext, err := authService.RevealUserAPIKey(t.Context(), domain.User{ID: userID, Role: domain.RoleAdmin}, key.ID)
	if err != nil {
		t.Fatalf("reveal via service: %v", err)
	}
	return plaintext
}

func TestRevealUserAPIKeyReturnsPlaintextForOwner(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, false)

	id := userAPIKeyID(t, authService, token, "my-key")
	plaintext := revealAsAdmin(t, authService, token, id)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, userAPIKeyRevealRequest(id, token, true))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), plaintext) {
		t.Fatalf("expected plaintext key in response, got %q", rr.Body.String())
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected Cache-Control no-store, got %q", rr.Header().Get("Cache-Control"))
	}
}

func TestRevealUserAPIKeyHidesPlaintextForOwner(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, false)

	id := userAPIKeyID(t, authService, token, "my-key")
	plaintext := revealAsAdmin(t, authService, token, id)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, userAPIKeyRevealRequest(id, token, false))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), plaintext) {
		t.Fatal("expected plaintext NOT to be in the hidden fragment")
	}
	if !strings.Contains(rr.Body.String(), "\u2022") {
		t.Fatalf("expected masked placeholder in hidden fragment, got %q", rr.Body.String())
	}
}

func TestRevealUserAPIKeyForbiddenForNonOwner(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, false, false)

	id := userAPIKeyID(t, authService, token, "alice-key")

	// bob is a different user; he must not reveal alice's key.
	bob, err := authService.Register(t.Context(), authsvc.RegisterRequest{Username: "bob", Password: "secret", IsAdmin: false})
	if err != nil {
		t.Fatalf("register bob: %v", err)
	}
	bobToken, err := authService.CreateSession(t.Context(), bob.ID)
	if err != nil {
		t.Fatalf("create bob session: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, userAPIKeyRevealRequest(id, bobToken, true))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-owner reveal, got %d", rr.Code)
	}
}

func TestRevealUserAPIKeyNotFound(t *testing.T) {
	handler, _, _, token := newAuthedHandler(t, true, false)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, userAPIKeyRevealRequest(999999, token, true))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown key, got %d", rr.Code)
	}
}

func TestUserAPIKeyDetailPageDoesNotLeakPlaintext(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, false)

	id := userAPIKeyID(t, authService, token, "my-key")
	plaintext := revealAsAdmin(t, authService, token, id)

	req := httptest.NewRequest(http.MethodGet, "/settings/api-keys/"+strconv.FormatInt(id, 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), plaintext) {
		t.Fatal("expected detail page GET to never contain the plaintext key")
	}
	if !strings.Contains(rr.Body.String(), "\u2022") {
		t.Fatalf("expected masked placeholder on detail page, got %q", rr.Body.String())
	}
	// Deletion lives on the detail page, not the settings table.
	if !strings.Contains(rr.Body.String(), "/settings/api-keys/"+strconv.FormatInt(id, 10)) {
		t.Fatalf("expected delete button on detail page, got %q", rr.Body.String())
	}
}

func TestUserAPIKeyDetailPageReadOnlyHidesRevealButton(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, true)

	id := userAPIKeyID(t, authService, token, "my-key")

	req := httptest.NewRequest(http.MethodGet, "/settings/api-keys/"+strconv.FormatInt(id, 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, "/reveal") {
		t.Fatal("expected no reveal button in read-only detail page")
	}
	if !strings.Contains(body, "Read-only") {
		t.Fatalf("expected read-only indicator, got %q", body)
	}
}

func TestAPIKeyEndpointsRedirectUnauthenticated(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, false)

	id := userAPIKeyID(t, authService, token, "my-key")

	// A request with no session cookie must be redirected to the login page,
	// never answered with a 500.
	req := httptest.NewRequest(http.MethodGet, "/settings/api-keys/"+strconv.FormatInt(id, 10), nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect to login for unauthenticated GET, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); !strings.Contains(loc, "/login") {
		t.Fatalf("expected redirect to login page, got %q", loc)
	}

	// htmx requests get 401 + HX-Redirect instead of a full redirect.
	req = httptest.NewRequest(http.MethodGet, "/settings/api-keys/"+strconv.FormatInt(id, 10), nil)
	req.Header.Set("HX-Request", "true")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unauthenticated htmx GET, got %d", rr.Code)
	}
	if loc := rr.Header().Get("HX-Redirect"); !strings.Contains(loc, "/login") {
		t.Fatalf("expected HX-Redirect to login page, got %q", loc)
	}
}

func TestCollectorAPIKeyCreateDeleteForbiddenForNonAdmin(t *testing.T) {
	handler, _, _, token := newAuthedHandler(t, false, false)

	create := httptest.NewRequest(http.MethodPost, "/settings/admin/collector-api-keys", strings.NewReader("name=prod-a"))
	create.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	create.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, create)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin collector key create, got %d (body: %s)", rr.Code, rr.Body.String())
	}

	del := httptest.NewRequest(http.MethodDelete, "/settings/admin/collector-api-keys/1", nil)
	del.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, del)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin collector key delete, got %d (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestUserAPIKeyDetailPageNotFoundForNonOwner(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, false, false)

	// alice owns a key; bob must get 404 (not 403) on her detail page so key
	// existence is not revealed.
	id := userAPIKeyID(t, authService, token, "alice-key")

	bob, err := authService.Register(t.Context(), authsvc.RegisterRequest{Username: "bob", Password: "secret", IsAdmin: false})
	if err != nil {
		t.Fatalf("register bob: %v", err)
	}
	bobToken, err := authService.CreateSession(t.Context(), bob.ID)
	if err != nil {
		t.Fatalf("create bob session: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/api-keys/"+strconv.FormatInt(id, 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: bobToken})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-owner detail GET (no existence leak), got %d (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestRevealUserAPIKeyLegacyShowsNotRevealable(t *testing.T) {
	ctx := t.Context()
	handler, authService, db, token := newAuthedHandler(t, true, false)

	userID, _, err := authService.ValidateSession(t.Context(), token)
	if err != nil {
		t.Fatalf("resolve session user: %v", err)
	}

	// Simulate a key created before reveal support: empty ciphertext.
	if err := db.Write(ctx, func(tx paneldb.DBTX) error {
		_, err := paneldb.New(tx).CreateUserAPIKey(ctx, paneldb.CreateUserAPIKeyParams{
			UserID:    userID,
			KeyHash:   "deadbeef",
			Name:      "legacy",
			CreatedAt: 1,
		})
		return err
	}); err != nil {
		t.Fatalf("insert legacy key: %v", err)
	}

	legacyID := userAPIKeyID(t, authService, token, "legacy")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, userAPIKeyRevealRequest(legacyID, token, true))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "\u2022") {
		t.Fatalf("expected masked placeholder in legacy fragment, got %q", body)
	}
	if !strings.Contains(body, "created before reveal support") {
		t.Fatalf("expected not-revealable message, got %q", body)
	}
}

func TestRevealUserAPIKeyDecryptFailureShowsError(t *testing.T) {
	ctx := t.Context()
	handler, authService, db, token := newAuthedHandler(t, true, false)

	userID, _, err := authService.ValidateSession(t.Context(), token)
	if err != nil {
		t.Fatalf("resolve session user: %v", err)
	}

	// Insert a key with garbage ciphertext so decryption fails.
	if err := db.Write(ctx, func(tx paneldb.DBTX) error {
		_, err := paneldb.New(tx).CreateUserAPIKey(ctx, paneldb.CreateUserAPIKeyParams{
			UserID:       userID,
			KeyHash:      "deadbeef",
			Name:         "broken",
			KeyEncrypted: "not-valid-base64!!",
			CreatedAt:    1,
		})
		return err
	}); err != nil {
		t.Fatalf("insert broken key: %v", err)
	}

	brokenID := userAPIKeyID(t, authService, token, "broken")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, userAPIKeyRevealRequest(brokenID, token, true))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "\u2022") {
		t.Fatalf("expected masked placeholder in decrypt-failure fragment, got %q", body)
	}
	if !strings.Contains(body, "Could not decrypt the key") {
		t.Fatalf("expected decrypt error message, got %q", body)
	}
}

func TestUserAPIKeyTableLinksToDetailPage(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, false)

	id := userAPIKeyID(t, authService, token, "my-key")

	req := httptest.NewRequest(http.MethodGet, "/settings/", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	link := "/settings/api-keys/" + strconv.FormatInt(id, 10)
	if !strings.Contains(body, link) {
		t.Fatalf("expected API key table rows to link to the detail page (%s), got %q", link, body)
	}
	// The delete button must no longer live in the table row.
	if strings.Contains(body, "hx-delete=\"/settings/api-keys/") {
		t.Fatalf("expected no delete button in the API key table, got %q", body)
	}
}

func TestSettingsPageShowsVersionFooter(t *testing.T) {
	handler, _, _, token := newAuthedHandler(t, true, false)

	req := httptest.NewRequest(http.MethodGet, "/settings/", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `class="mt-12 pt-6 border-t border-bg-secondary/50 text-xs text-fg-secondary/60"`) {
		t.Fatalf("expected version footer element on the settings page, got %q", body)
	}
	// Tests build without ldflags, so panelbuild.Version is "unknown".
	if !strings.Contains(body, "tracer "+panelbuild.Version) {
		t.Fatalf("expected version footer %q, got %q", "tracer "+panelbuild.Version, body)
	}
}

func TestCreateForgejoNotifier(t *testing.T) {
	repoValidated := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/user":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"login":"x"}`))
		case r.URL.Path == "/api/v1/user/orgs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/users/x/repos":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"name":"barse"}]`))
		case r.URL.Path == "/api/v1/repos/x/barse":
			repoValidated = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"full_name":"x/barse"}`))
		case r.URL.Path == "/api/v1/repos/x/barse/labels":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":3,"name":"bug"},{"id":5,"name":"ops"}]`))
		case r.URL.Path == "/api/v1/repos/x/barse/milestones":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":7,"title":"v2.0"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	handler, _, _, token, notifyService := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{
		"forgejo": func(name, config string) (notifysvc.Notifier, error) {
			return forgejo.New(name, config, forgejo.NewCache(time.Minute))
		},
	})

	body := fmt.Sprintf("type=forgejo&name=issues&base_url=%s&token=sekret&owner_repo=x/barse&labels=bug,ops&milestone_name=v2.0", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after create, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if !repoValidated {
		t.Fatal("expected the welcome flow to validate the repo connection")
	}

	// Verify the persisted config through the public service API.
	owner := domain.User{ID: 1, Role: domain.RoleAdmin}
	setup, err := notifyService.GetSetup(t.Context(), owner, 1)
	if err != nil {
		t.Fatalf("get notifier setup: %v", err)
	}
	for _, want := range []string{`"base_url":"` + srv.URL + `"`, `"token":"sekret"`, `"owner":"x"`, `"repo":"barse"`, `"labels":[3,5]`, `"milestone":7`} {
		if !strings.Contains(setup.Config, want) {
			t.Errorf("expected %s in persisted config, got %q", want, setup.Config)
		}
	}
}

func TestNotifierDetailPageMasksForgejoToken(t *testing.T) {
	srv := forgejoTestServer(t)
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{
		"forgejo": func(name, config string) (notifysvc.Notifier, error) {
			return forgejo.New(name, config, forgejo.NewCache(time.Minute))
		},
	})

	body := fmt.Sprintf("type=forgejo&name=issues&base_url=%s&token=sekret&owner_repo=x/barse", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after create, got %d (body: %s)", rr.Code, rr.Body.String())
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/settings/notifiers/1", nil)
	detailReq.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	detailRR := httptest.NewRecorder()
	handler.ServeHTTP(detailRR, detailReq)
	if detailRR.Code != http.StatusOK {
		t.Fatalf("expected 200 for notifier detail, got %d (body: %s)", detailRR.Code, detailRR.Body.String())
	}

	out := detailRR.Body.String()
	if strings.Contains(out, "sekret") {
		t.Errorf("notifier detail page leaks the plaintext token, got: %s", out)
	}
	if !strings.Contains(out, "••••kret") {
		t.Errorf("expected masked token (••••kret) on notifier detail page, got: %s", out)
	}
	if got := detailRR.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("expected Cache-Control: no-store on notifier detail, got %q", got)
	}
}

func TestCreateForgejoNotifierWithoutLabelsOrMilestone(t *testing.T) {
	srv := forgejoTestServer(t)
	handler, _, _, token, notifyService := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{
		"forgejo": func(name, config string) (notifysvc.Notifier, error) {
			return forgejo.New(name, config, forgejo.NewCache(time.Minute))
		},
	})

	body := fmt.Sprintf("type=forgejo&name=plain&base_url=%s&token=sekret&owner_repo=x/barse", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after create, got %d (body: %s)", rr.Code, rr.Body.String())
	}

	setup, err := notifyService.GetSetup(t.Context(), domain.User{ID: 1, Role: domain.RoleAdmin}, 1)
	if err != nil {
		t.Fatalf("get notifier setup: %v", err)
	}
	if strings.Contains(setup.Config, "labels") || strings.Contains(setup.Config, "milestone") {
		t.Errorf("expected labels/milestone omitted from config, got %q", setup.Config)
	}
}

func TestCreateForgejoNotifierMissingFields(t *testing.T) {
	handler, _, _, token := newAuthedHandler(t, true, false)

	body := "type=forgejo&name=issues&base_url=https://git.naqerl.com&token=sekret&owner_repo=x"
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for missing repo, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if !strings.Contains(out, "enter a repository as owner/repo") || !strings.Contains(out, `id="forgejo-owner-repo-error"`) {
		t.Errorf("expected owner/repo label error, got: %s", out)
	}
}

// forgejoTestServer serves the owner, repo, labels and milestones endpoints
// used by the lookup handlers and the welcome validation.
func forgejoTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/user":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"login":"x"}`))
		case r.URL.Path == "/api/v1/user/orgs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"name":"naqerl"}]`))
		case r.URL.Path == "/api/v1/users/x/repos":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"name":"barse"},{"name":"tracer"}]`))
		case r.URL.Path == "/api/v1/orgs/naqerl/repos":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"name":"website"}]`))
		case r.URL.Path == "/api/v1/repos/x/barse":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"full_name":"x/barse"}`))
		case r.URL.Path == "/api/v1/repos/x/barse/labels":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":3,"name":"bug"},{"id":5,"name":"ops"}]`))
		case r.URL.Path == "/api/v1/repos/x/barse/milestones":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"id":7,"title":"v2.0"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestForgejoLookupReturnsOwnerRepos(t *testing.T) {
	srv := forgejoTestServer(t)
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	body := fmt.Sprintf("base_url=%s&token=sekret", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers/forgejo/lookup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	for _, want := range []string{`value="x/barse"`, `value="x/tracer"`, `value="naqerl/website"`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected owner/repo option %s, got: %s", want, out)
		}
	}
	if !strings.Contains(out, "authenticated (found 3 repositories)") {
		t.Errorf("expected authenticated status, got: %s", out)
	}
	if !strings.Contains(out, `id="forgejo_owner_repo"`) {
		t.Errorf("expected merged owner/repo input, got: %s", out)
	}
	// The repo fetching indicator is wired to the options request.
	if !strings.Contains(out, `id="forgejo-repo-fetching"`) {
		t.Errorf("expected repo fetching indicator, got: %s", out)
	}
	if !strings.Contains(out, `hx-indicator="#forgejo-repo-fetching"`) {
		t.Errorf("expected owner/repo input to reference the repo indicator, got: %s", out)
	}
	// The status is rendered into the out-of-band auth status element.
	if !strings.Contains(out, `id="forgejo-auth-status" hx-swap-oob="true"`) {
		t.Errorf("expected OOB auth status element, got: %s", out)
	}
	// No label/milestone inputs until a repo is selected.
	if strings.Contains(out, `id="forgejo_labels"`) || strings.Contains(out, `id="forgejo_milestone"`) {
		t.Errorf("expected no label/milestone inputs before repo selection, got: %s", out)
	}
}

func TestForgejoLookupUnauthorizedShowsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	body := fmt.Sprintf("base_url=%s&token=badtoken", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers/forgejo/lookup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 fragment with error, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if !strings.Contains(out, "invalid Forgejo credentials") {
		t.Errorf("expected short unauthorized error, got: %s", out)
	}
	if strings.Contains(out, "unauthorized</p>") || strings.Contains(out, `"unauthorized"`) {
		t.Errorf("expected the remote body not to be echoed, got: %s", out)
	}
}

func TestForgejoLookupMissingFields(t *testing.T) {
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers/forgejo/lookup", strings.NewReader("base_url=https://git.naqerl.com"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with empty fragment for missing token, got %d", rr.Code)
	}
}

func TestForgejoFormInitialStateAndFetchingIndicator(t *testing.T) {
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	req := httptest.NewRequest(http.MethodGet, "/settings/notifiers/new?type=forgejo", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()

	// Only the name, base URL and token fields are visible initially.
	for _, want := range []string{`id="notifier_name"`, `id="forgejo_base_url"`, `id="forgejo_token"`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected initial field %s, got: %s", want, out)
		}
	}
	// Dynamic fields must NOT be present until auth/repo selection.
	for _, notWant := range []string{`id="forgejo_owner_repo"`, `id="forgejo_labels"`, `id="forgejo_milestone"`} {
		if strings.Contains(out, notWant) {
			t.Errorf("expected %s to be hidden initially, got: %s", notWant, out)
		}
	}

	// The fetching indicator exists after the token field and is wired to the
	// lookup request via hx-indicator on both credential inputs.
	if !strings.Contains(out, `id="forgejo-fetching"`) {
		t.Errorf("expected fetching indicator element, got: %s", out)
	}
	if !strings.Contains(out, `class="htmx-indicator`) {
		t.Errorf("expected indicator to carry the htmx-indicator class, got: %s", out)
	}
	if strings.Count(out, `hx-indicator="#forgejo-fetching"`) != 2 {
		t.Errorf("expected hx-indicator on both base_url and token inputs, got: %s", out)
	}

	// The auth status placeholder sits right after the token field.
	if !strings.Contains(out, `id="forgejo-auth-status"`) {
		t.Errorf("expected auth status element after token field, got: %s", out)
	}

	// The token placeholder documents the scopes the API token needs.
	if !strings.Contains(out, `placeholder="API token — scopes: read:user, read:organization, read:repository, write:issue"`) {
		t.Errorf("expected token placeholder to document required scopes, got: %s", out)
	}
}

func TestNotifierFormsHaveInputLabels(t *testing.T) {
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	for _, tc := range []struct {
		typ    string
		labels []string
	}{
		{"telegram", []string{"Name", "Bot token", "Chat ID"}},
		{"ntfy", []string{"Name", "Server URL", "Topic"}},
		{"forgejo", []string{"Name", "Forgejo base URL", "API token"}},
	} {
		req := httptest.NewRequest(http.MethodGet, "/settings/notifiers/new?type="+tc.typ, nil)
		req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d (body: %s)", tc.typ, rr.Code, rr.Body.String())
		}
		out := rr.Body.String()
		for _, label := range tc.labels {
			if !strings.Contains(out, ">"+label) {
				t.Errorf("%s: expected label %q above input, got: %s", tc.typ, label, out)
			}
		}
	}
}

func TestForgejoOptionsLoadsSelects(t *testing.T) {
	srv := forgejoTestServer(t)
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	body := fmt.Sprintf("base_url=%s&token=sekret&owner_repo=x/barse", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers/forgejo/options", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	// Check for datalist options with label names
	if !strings.Contains(out, `value="bug"`) || !strings.Contains(out, `value="ops"`) {
		t.Errorf("expected label datalist options, got: %s", out)
	}
	if !strings.Contains(out, `value="v2.0"`) {
		t.Errorf("expected milestone datalist option, got: %s", out)
	}
	// Counts are shown in the label text, not as a separate status line.
	if !strings.Contains(out, "Labels (found 2)") {
		t.Errorf("expected label count in label text, got: %s", out)
	}
	if !strings.Contains(out, "Milestone (found 1)") {
		t.Errorf("expected milestone count in label text, got: %s", out)
	}
	if strings.Contains(out, "2 labels, 1 milestones") {
		t.Errorf("expected no separate count status line, got: %s", out)
	}
	// Placeholders preview the real found names, not mock text.
	if !strings.Contains(out, `placeholder="bug, ops"`) {
		t.Errorf("expected labels placeholder from found data, got: %s", out)
	}
	if !strings.Contains(out, `placeholder="v2.0"`) {
		t.Errorf("expected milestone placeholder from found data, got: %s", out)
	}
	if strings.Contains(out, "bug, enhancement") || strings.Contains(out, "v1.0, v2.0") {
		t.Errorf("expected no mock placeholder text, got: %s", out)
	}
	// Once a repo is loaded with found options the inputs must be editable
	// (no disabled attribute on the labels/milestone inputs).
	if strings.Contains(out, "id=\"forgejo_labels\"") && strings.Contains(out, "placeholder=\"bug, ops\" disabled") {
		t.Errorf("expected labels input to be enabled when labels exist, got: %s", out)
	}
	if !strings.Contains(out, `id="forgejo_labels"`) || !strings.Contains(out, `id="forgejo_milestone"`) {
		t.Errorf("expected label/milestone inputs rendered, got: %s", out)
	}
}

func TestForgejoOptionsDisablesEmptyLabelsAndMilestones(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/user":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"login":"x"}`))
		case r.URL.Path == "/api/v1/user/orgs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/users/x/repos":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"name":"barse"}]`))
		case r.URL.Path == "/api/v1/repos/x/barse":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"full_name":"x/barse"}`))
		case r.URL.Path == "/api/v1/repos/x/barse/labels":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/repos/x/barse/milestones":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	body := fmt.Sprintf("base_url=%s&token=sekret&owner_repo=x/barse", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers/forgejo/options", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if !strings.Contains(out, "Labels (found 0)") || !strings.Contains(out, "Milestone (found 0)") {
		t.Errorf("expected zero counts in label text, got: %s", out)
	}
	// Both inputs must be disabled and have no placeholder preview.
	if !strings.Contains(out, `placeholder="" disabled`) {
		t.Errorf("expected empty placeholder and disabled attribute for empty fields, got: %s", out)
	}
	if strings.Contains(out, "placeholder=\"bug") || strings.Contains(out, "placeholder=\"v1") {
		t.Errorf("expected no placeholder preview when nothing found, got: %s", out)
	}
}

func TestForgejoOptionsRejectsUnknownOwnerRepo(t *testing.T) {
	srv := forgejoTestServer(t)
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	body := fmt.Sprintf("base_url=%s&token=sekret&owner_repo=ghost/nope", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers/forgejo/options", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for unknown owner/repo, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if !strings.Contains(out, "does not exist") || !strings.Contains(out, "ghost/nope") {
		t.Errorf("expected does-not-exist error naming the owner/repo, got: %s", out)
	}
	if !strings.Contains(out, `id="forgejo-owner-repo-error"`) {
		t.Errorf("expected error targeted at the owner/repo label, got: %s", out)
	}
}

func TestCreateForgejoNotifierRejectsUnknownOwnerRepo(t *testing.T) {
	srv := forgejoTestServer(t)
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	body := fmt.Sprintf("type=forgejo&name=issues&base_url=%s&token=sekret&owner_repo=ghost/nope", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for unknown owner/repo, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if !strings.Contains(out, "does not exist") || !strings.Contains(out, "ghost/nope") {
		t.Errorf("expected does-not-exist error naming the owner/repo, got: %s", out)
	}
}

func TestCreateForgejoNotifierRejectsUnknownLabel(t *testing.T) {
	srv := forgejoTestServer(t)
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	body := fmt.Sprintf("type=forgejo&name=issues&base_url=%s&token=sekret&owner_repo=x/barse&labels=bug,ghost", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for unknown label, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if !strings.Contains(out, "label") || !strings.Contains(out, "ghost") || !strings.Contains(out, "does not exist") {
		t.Errorf("expected does-not-exist error naming the label, got: %s", out)
	}
	if !strings.Contains(out, `id="forgejo-labels-error"`) {
		t.Errorf("expected error targeted at the labels label, got: %s", out)
	}
}

func TestCreateForgejoNotifierRejectsUnknownMilestone(t *testing.T) {
	srv := forgejoTestServer(t)
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	body := fmt.Sprintf("type=forgejo&name=issues&base_url=%s&token=sekret&owner_repo=x/barse&milestone_name=ghost", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for unknown milestone, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if !strings.Contains(out, "milestone") || !strings.Contains(out, "ghost") || !strings.Contains(out, "does not exist") {
		t.Errorf("expected does-not-exist error naming the milestone, got: %s", out)
	}
	if !strings.Contains(out, `id="forgejo-milestone-error"`) {
		t.Errorf("expected error targeted at the milestone label, got: %s", out)
	}
}

func TestNotifierFormsHaveRequiredAsterisks(t *testing.T) {
	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{})

	for _, tc := range []struct {
		typ    string
		fields int
	}{
		{"telegram", 3}, // Name, Bot token, Chat ID
		{"ntfy", 3},     // Name, Server URL, Topic
		{"forgejo", 3},  // Name, Forgejo base URL, API token
	} {
		req := httptest.NewRequest(http.MethodGet, "/settings/notifiers/new?type="+tc.typ, nil)
		req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d (body: %s)", tc.typ, rr.Code, rr.Body.String())
		}
		out := rr.Body.String()
		if got := strings.Count(out, `aria-hidden="true">*</span>`); got != tc.fields {
			t.Errorf("%s: expected %d required asterisks, got %d", tc.typ, tc.fields, got)
		}
	}
}

func TestHeadAllows422Swap(t *testing.T) {
	var b strings.Builder
	if err := templates.Head().Render(t.Context(), &b); err != nil {
		t.Fatalf("render head: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, `"code":"422","swap":true`) {
		t.Errorf("expected 422 marked swap:true in responseHandling config, got: %s", out)
	}
	// The meta config is the single mechanism; no redundant JS listener.
	if strings.Contains(out, "beforeSwap") {
		t.Errorf("expected no beforeSwap listener, got: %s", out)
	}
}

func TestCreateForgejoNotifierRejectedByWelcomeValidation(t *testing.T) {
	// The list endpoints succeed so the owner/repo check passes; only the
	// final welcome repo validation fails, exercising the ErrWelcomeValidation
	// branch of createNotifier.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/user":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"login":"x"}`))
		case r.URL.Path == "/api/v1/user/orgs":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[]`))
		case r.URL.Path == "/api/v1/users/x/repos":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"name":"barse"}]`))
		case r.URL.Path == "/api/v1/repos/x/barse":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	handler, _, _, token, _ := newAuthedHandlerWithFactories(t, true, false, map[string]notifysvc.FromRawJSON{
		"forgejo": func(name, config string) (notifysvc.Notifier, error) {
			return forgejo.New(name, config, forgejo.NewCache(time.Minute))
		},
	})

	body := fmt.Sprintf("type=forgejo&name=issues&base_url=%s&token=badtoken&owner_repo=x/barse", srv.URL)
	req := httptest.NewRequest(http.MethodPost, "/settings/notifiers", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for rejected credentials, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	out := rr.Body.String()
	if !strings.Contains(out, "404") {
		t.Fatalf("expected the repo 404 to surface in the response body, got %q", out)
	}
	if strings.Contains(out, "could not send welcome notification") {
		t.Errorf("expected the service-level prefix stripped from the message, got: %s", out)
	}
	if !strings.Contains(out, `id="forgejo-auth-status"`) {
		t.Errorf("expected error targeted at the token label, got: %s", out)
	}
}
