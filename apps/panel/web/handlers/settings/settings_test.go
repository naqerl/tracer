package settings

import (
	"context"
	"fmt"
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

// noopRecorder satisfies forgejo.IssueRecorder without side effects.
type noopRecorder struct{}

func (noopRecorder) RecordGroupIssue(context.Context, string, string, int64, int64, string) error {
	return nil
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
			return forgejo.New(name, config, noopRecorder{})
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
	owner := usersvc.User{ID: 1, IsAdmin: true}
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
			return forgejo.New(name, config, noopRecorder{})
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
			return forgejo.New(name, config, noopRecorder{})
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

	setup, err := notifyService.GetSetup(t.Context(), usersvc.User{ID: 1, IsAdmin: true}, 1)
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
			return forgejo.New(name, config, noopRecorder{})
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
