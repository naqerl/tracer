package settings

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	paneldb "tracer/apps/panel/db"
	"tracer/apps/panel/internal/config"
	"tracer/apps/panel/internal/dbmanager"
	authsvc "tracer/apps/panel/internal/services/auth"
	ingestsvc "tracer/apps/panel/internal/services/ingest"
	notifysvc "tracer/apps/panel/internal/services/notify"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/testutil"
	authmw "tracer/apps/panel/web/middleware/auth"
)

// newAuthedHandler builds the settings mux behind the auth middleware and
// registers a user (admin or not). It returns the handler, the auth service
// (to create API keys), the panel DB handle (for direct inserts) and a session
// token cookie.
func newAuthedHandler(t *testing.T, isAdmin, readOnly bool) (http.Handler, *authsvc.Service, *dbmanager.DBHandle, string) {
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
	notifyService := notifysvc.New(db, userService, map[string]notifysvc.FromRawJSON{})

	mux := http.NewServeMux()
	mux.Handle("/settings/", http.StripPrefix("/settings", InitMux(authService, userService, ingestService, dbMgr, notifyService, cfg, readOnly)))
	handler := authmw.BuildAuthMiddleware(authService, userService)(mux)

	return handler, authService, db, token
}

func revealRequest(name, token string, revealed bool) *http.Request {
	body := "revealed=0"
	if revealed {
		body = "revealed=1"
	}
	req := httptest.NewRequest(http.MethodPost, "/settings/admin/collector-api-keys/"+name+"/reveal", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	return req
}

func TestRevealCollectorAPIKeyReturnsPlaintextForAdmin(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, false)

	key, _, err := authService.GenerateCollectorAPIKey(t.Context(), authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest("prod-a", token, true))

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

	key, _, err := authService.GenerateCollectorAPIKey(t.Context(), authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest("prod-a", token, false))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), key) {
		t.Fatal("expected plaintext NOT to be in the hidden fragment")
	}
	if !strings.Contains(rr.Body.String(), "****") {
		t.Fatalf("expected masked suffix in hidden fragment, got %q", rr.Body.String())
	}
}

func TestRevealCollectorAPIKeyForbiddenForNonAdmin(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, false, false)

	if _, _, err := authService.GenerateCollectorAPIKey(t.Context(), authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest("prod-a", token, true))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin reveal, got %d", rr.Code)
	}
}

func TestRevealCollectorAPIKeyNotFound(t *testing.T) {
	handler, _, _, token := newAuthedHandler(t, true, false)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest("ghost", token, true))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown key, got %d", rr.Code)
	}
}

func TestCollectorAPIKeyDetailPageDoesNotLeakPlaintext(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, false)

	key, _, err := authService.GenerateCollectorAPIKey(t.Context(), authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/prod-a", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), key) {
		t.Fatal("expected detail page GET to never contain the plaintext key")
	}
	if !strings.Contains(rr.Body.String(), "****") {
		t.Fatalf("expected masked suffix on detail page, got %q", rr.Body.String())
	}
}

func TestCollectorAPIKeyDetailPageReadOnlyHidesRevealButton(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true, true)

	if _, _, err := authService.GenerateCollectorAPIKey(t.Context(), authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"}); err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/prod-a", nil)
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
	handler, _, db, token := newAuthedHandler(t, true, false)

	// Simulate a key created before reveal support: empty ciphertext.
	if err := db.Write(ctx, func(tx paneldb.DBTX) error {
		_, err := paneldb.New(tx).CreateCollectorAPIKey(ctx, paneldb.CreateCollectorAPIKeyParams{
			Name:      "legacy",
			KeyHash:   "deadbeef",
			KeySuffix: "c0ffee",
			CreatedAt: 1,
		})
		return err
	}); err != nil {
		t.Fatalf("insert legacy key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest("legacy", token, true))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "****c0ffee") {
		t.Fatalf("expected masked suffix in legacy fragment, got %q", body)
	}
	if !strings.Contains(body, "created before reveal support") {
		t.Fatalf("expected not-revealable message, got %q", body)
	}
}

func TestRevealCollectorAPIKeyDecryptFailureShowsError(t *testing.T) {
	ctx := t.Context()
	handler, _, db, token := newAuthedHandler(t, true, false)

	// Insert a key with garbage ciphertext so decryption fails.
	if err := db.Write(ctx, func(tx paneldb.DBTX) error {
		_, err := paneldb.New(tx).CreateCollectorAPIKey(ctx, paneldb.CreateCollectorAPIKeyParams{
			Name:         "broken",
			KeyHash:      "deadbeef",
			KeySuffix:    "beef00",
			KeyEncrypted: "not-valid-base64!!",
			CreatedAt:    1,
		})
		return err
	}); err != nil {
		t.Fatalf("insert broken key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, revealRequest("broken", token, true))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "****beef00") {
		t.Fatalf("expected masked suffix in decrypt-failure fragment, got %q", body)
	}
	if !strings.Contains(body, "Could not decrypt the key") {
		t.Fatalf("expected decrypt error message, got %q", body)
	}
}
