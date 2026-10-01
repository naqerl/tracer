package settings

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	paneldb "tracer/apps/panel/db"
	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
)

// installCommandRequest builds a request to the install-command endpoint.
func installCommandRequest(id int64, token string, htmx bool) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/settings/admin/collector-api-keys/"+strconv.FormatInt(id, 10)+"/install-command", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	return req
}

// TestCollectorInstallCommandReturnsCopyPasteableCommand proves a user can get
// the command with the key inlined in one request, without a reveal step, and
// that the response is the bare command the clipboard gets — never markup.
func TestCollectorInstallCommandReturnsCopyPasteableCommand(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true)

	created, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, installCommandRequest(created.Key.ID, token, false))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	want := fmt.Sprintf(
		"curl -fsSL 'https://tracer.example.com/panel/collectors/prod-a/install-script' -H %s | sudo bash",
		shellQuote("X-API-Key: "+created.Plaintext),
	)
	if body != want {
		t.Fatalf("expected the single-line command %q, got %q", want, body)
	}
	if strings.Contains(body, "<API_KEY>") {
		t.Fatalf("command still has a placeholder: %q", body)
	}
	for _, markup := range []string{"<div", "<code", "</pre>"} {
		if strings.Contains(body, markup) {
			t.Fatalf("clipboard body must be plain text, got %q", body)
		}
	}
	if strings.Contains(body, "\n") {
		t.Fatalf("expected a single-line command, got %q", body)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("expected text/plain for clipboard use, got %q", ct)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected no-store so the plaintext is not cached, got %q", rr.Header().Get("Cache-Control"))
	}
}

// TestCollectorCopyButtonRequestsPlainText pins the request the copy script
// makes: no HX-Request, because that header returns the reveal fragment and the
// clipboard would get markup instead of the command.
func TestCollectorCopyButtonRequestsPlainText(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true)

	created, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/"+strconv.FormatInt(created.Key.ID, 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "fetch(url, { method: 'POST' })") {
		t.Fatal("the copy script must POST without an HX-Request header")
	}
	if strings.Contains(body, "'HX-Request': 'true'") {
		t.Fatal("the copy script must not ask for the reveal fragment")
	}
}

// TestCollectorInstallCommandFragmentForReveal covers the htmx path that swaps
// the revealed command onto the page.
func TestCollectorInstallCommandFragmentForReveal(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true)

	created, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, installCommandRequest(created.Key.ID, token, true))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, created.Plaintext) {
		t.Fatalf("expected plaintext in the fragment, got %q", body)
	}
	if !strings.Contains(body, `id="collector-install-command"`) {
		t.Fatalf("fragment must carry the swap target id, got %q", body)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected no-store, got %q", rr.Header().Get("Cache-Control"))
	}
}

// TestCollectorDetailPageHidesCopyForNotRevealableKey keeps the Copy button off
// the detail page when the install-command endpoint can only answer 410.
func TestCollectorDetailPageHidesCopyForNotRevealableKey(t *testing.T) {
	ctx := t.Context()
	handler, authService, db, token := newAuthedHandler(t, true)

	// A key created before reveal support: no ciphertext to decrypt.
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

	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/"+strconv.FormatInt(collectorKeyID(t, authService, "legacy"), 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "This key cannot be shown again") {
		t.Fatalf("expected the not-revealable notice, got %q", body)
	}
	for _, button := range []string{`onclick="copyCollectorOnboarding(this)"`, ">Show</button>"} {
		if strings.Contains(body, button) {
			t.Errorf("expected no %q button for a key that cannot be revealed", button)
		}
	}
}

// TestCollectorDetailPageOffersCopyForRevealableKey is the counterpart: a
// revealable key still gets both buttons.
func TestCollectorDetailPageOffersCopyForRevealableKey(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true)

	created, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/"+strconv.FormatInt(created.Key.ID, 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, button := range []string{`onclick="copyCollectorOnboarding(this)"`, ">Show</button>"} {
		if !strings.Contains(body, button) {
			t.Errorf("expected the %q button for a revealable key", button)
		}
	}
}

// TestCollectorInstallCommandRejectsUnusableKeyName keeps the panel from handing
// out a command its own install-script endpoint would refuse: a key stored under
// the old, looser name rules cannot be a collector name in the URL.
func TestCollectorInstallCommandRejectsUnusableKeyName(t *testing.T) {
	ctx := t.Context()
	handler, authService, db, token := newAuthedHandler(t, true)

	if err := db.Write(ctx, func(tx paneldb.DBTX) error {
		_, err := paneldb.New(tx).CreateCollectorAPIKey(ctx, paneldb.CreateCollectorAPIKeyParams{
			Name:      "prod key",
			KeyHash:   "deadbeef",
			CreatedAt: 1,
		})
		return err
	}); err != nil {
		t.Fatalf("insert key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, installCommandRequest(collectorKeyID(t, authService, "prod key"), token, false))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a name the install-script endpoint cannot serve, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "install-script") {
		t.Fatalf("a rejected key must not produce a command, got %q", rr.Body.String())
	}
}

// TestCollectorDetailPageHidesInstallForUnusableKeyName is the same condition on
// the page: no copy button pointing at a command the endpoint refuses.
func TestCollectorDetailPageHidesInstallForUnusableKeyName(t *testing.T) {
	ctx := t.Context()
	handler, authService, db, token := newAuthedHandler(t, true)

	if err := db.Write(ctx, func(tx paneldb.DBTX) error {
		_, err := paneldb.New(tx).CreateCollectorAPIKey(ctx, paneldb.CreateCollectorAPIKeyParams{
			Name:      "prod key",
			KeyHash:   "deadbeef",
			CreatedAt: 1,
		})
		return err
	}); err != nil {
		t.Fatalf("insert key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/"+strconv.FormatInt(collectorKeyID(t, authService, "prod key"), 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "cannot be used in an install command") {
		t.Fatalf("expected the unusable-name notice, got %q", body)
	}
	for _, fragment := range []string{"install-script", `onclick="copyCollectorOnboarding(this)"`} {
		if strings.Contains(body, fragment) {
			t.Errorf("expected no %q for a key that cannot produce a command", fragment)
		}
	}
}

// TestCollectorInstallCommandForbiddenForNonAdmin keeps the endpoint admin-only.
func TestCollectorInstallCommandForbiddenForNonAdmin(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, false)

	created, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, installCommandRequest(created.Key.ID, token, false))

	if rr.Code != http.StatusForbidden && rr.Code != http.StatusNotFound {
		t.Fatalf("expected 403 or 404 for non-admin, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), created.Plaintext) {
		t.Fatal("non-admin response leaked the plaintext key")
	}
}

// TestCollectorInstallCommandNotFound keeps an unknown id from looking valid.
func TestCollectorInstallCommandNotFound(t *testing.T) {
	handler, _, _, token := newAuthedHandler(t, true)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, installCommandRequest(999999, token, false))

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown key, got %d", rr.Code)
	}
}

// TestCollectorAPIKeyDetailPageHasOneClickCopy checks the page ships the copy
// button and the masked command, without the plaintext.
func TestCollectorAPIKeyDetailPageHasOneClickCopy(t *testing.T) {
	handler, authService, _, token := newAuthedHandler(t, true)

	created, err := authService.GenerateCollectorAPIKey(t.Context(), domain.System{}, authsvc.GenerateCollectorAPIKeyRequest{Name: "prod-a"})
	if err != nil {
		t.Fatalf("generate collector api key: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/settings/admin/collector-api-keys/"+strconv.FormatInt(collectorKeyID(t, authService, "prod-a"), 10), nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	body := rr.Body.String()
	if strings.Contains(body, created.Plaintext) {
		t.Fatal("detail page must not embed the plaintext key")
	}
	if strings.Contains(body, "Reveal the API key first") {
		t.Fatal("the reveal-first copy prompt must be gone")
	}
	for _, want := range []string{"copyCollectorOnboarding", "install-command", "Copy"} {
		if !strings.Contains(body, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}
