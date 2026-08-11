package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	paneldb "tracer/apps/panel/db"
	authsvc "tracer/apps/panel/internal/services/auth"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/testutil"
)

func TestMiddlewareReissuesCookieOnSessionRefresh(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)
	authService := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	user, err := authService.Register(ctx, authsvc.RegisterRequest{
		Username: "admin",
		Password: "password",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token, err := authService.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	// Give the session 11h remaining (past its half-life, not expired) so
	// validation extends it and the middleware re-issues the cookie.
	if err := db.Write(ctx, func(tx paneldb.DBTX) error {
		return paneldb.New(tx).RefreshSession(ctx, paneldb.RefreshSessionParams{
			ExpiresAt: time.Now().Add(authsvc.SessionDuration/2 - time.Hour).Unix(),
			ID:        token,
		})
	}); err != nil {
		t.Fatalf("backdate session: %v", err)
	}

	mw := BuildAuthMiddleware(authService, usersvc.NewService(db))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rr := httptest.NewRecorder()

	var gotUserID int64
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserID, _ = GetUserID(r)
	})).ServeHTTP(rr, req)

	if gotUserID != user.ID {
		t.Fatalf("expected authenticated user %d, got %d", user.ID, gotUserID)
	}
	setCookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "tracer_session="+token) {
		t.Fatalf("expected refreshed session cookie, got %q", setCookie)
	}
	if !strings.Contains(setCookie, "Max-Age=86400") {
		t.Fatalf("expected full Max-Age on refreshed cookie, got %q", setCookie)
	}
}

func TestMiddlewareDoesNotReissueCookieEarly(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)
	authService := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	user, err := authService.Register(ctx, authsvc.RegisterRequest{
		Username: "admin",
		Password: "password",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token, err := authService.CreateSession(ctx, user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	mw := BuildAuthMiddleware(authService, usersvc.NewService(db))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rr := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rr, req)

	if got := rr.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("expected no cookie re-issue for a fresh session, got %q", got)
	}
}
