package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	paneldb "tracer/apps/panel/db"
	"tracer/apps/panel/internal/domain"
	authsvc "tracer/apps/panel/internal/services/auth"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/testutil"
)

func TestMiddlewareResolvesAdminActor(t *testing.T) {
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

	var gotActor domain.Actor
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotActor = GetActor(r)
	})).ServeHTTP(rr, req)

	u, ok := gotActor.(domain.User)
	if !ok {
		t.Fatalf("expected domain.User actor, got %T", gotActor)
	}
	if u.ID != user.ID {
		t.Fatalf("expected user %d, got %d", user.ID, u.ID)
	}
	if u.Role != domain.RoleAdmin {
		t.Fatalf("expected admin role, got %q", u.Role)
	}
}

func TestMiddlewareResolvesRegularUserActor(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)
	authService := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	user, err := authService.Register(ctx, authsvc.RegisterRequest{
		Username: "alice",
		Password: "password",
		IsAdmin:  false,
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

	var gotActor domain.Actor
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotActor = GetActor(r)
	})).ServeHTTP(rr, req)

	u, ok := gotActor.(domain.User)
	if !ok {
		t.Fatalf("expected domain.User actor, got %T", gotActor)
	}
	if u.Role != domain.RoleUser {
		t.Fatalf("expected user role, got %q", u.Role)
	}
}

func TestMiddlewareUnauthenticatedActor(t *testing.T) {
	db := testutil.NewTestDB(t)
	authService := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	mw := BuildAuthMiddleware(authService, usersvc.NewService(db))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	var gotActor domain.Actor
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotActor = GetActor(r)
	})).ServeHTTP(rr, req)

	if _, ok := gotActor.(domain.Unauthenticated); !ok {
		t.Fatalf("expected domain.Unauthenticated actor, got %T", gotActor)
	}
}

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
		if u, ok := GetActor(r).(domain.User); ok {
			gotUserID = u.ID
		}
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

func TestRequireUserReturnsUser(t *testing.T) {
	ctx := t.Context()
	db := testutil.NewTestDB(t)
	authService := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	user, err := authService.Register(ctx, authsvc.RegisterRequest{
		Username: "alice",
		Password: "password",
		IsAdmin:  false,
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

	var got domain.User
	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, err := RequireUser(w, r)
		if err == nil {
			got = u
		}
	})).ServeHTTP(rr, req)

	if err != nil {
		t.Fatalf("require user: %v", err)
	}
	if got.ID != user.ID {
		t.Fatalf("expected user %d, got %d", user.ID, got.ID)
	}
	if got.Role != domain.RoleUser {
		t.Fatalf("expected user role, got %q", got.Role)
	}
}

func TestRequireUserRedirectsAnonymous(t *testing.T) {
	db := testutil.NewTestDB(t)
	authService := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	mw := BuildAuthMiddleware(authService, usersvc.NewService(db))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()

	mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := RequireUser(w, r); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("expected ErrUnauthorized, got %v", err)
		}
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect to login, got %d", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != defaultLoginPath {
		t.Fatalf("expected redirect to %q, got %q", defaultLoginPath, loc)
	}
}
