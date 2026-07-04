package topology

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"git.naqerl.com/goext/rwdb"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	authsvc "tracer/apps/panel/internal/services/auth"
	topologysvc "tracer/apps/panel/internal/services/topology"
	usersvc "tracer/apps/panel/internal/services/user"
	panelmigrations "tracer/apps/panel/sql/panel"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/libs/types"
)

func newTestDB(t *testing.T) *rwdb.DBHandle {
	t.Helper()

	db, err := sql.Open("sqlite", "file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, panelmigrations.Embed)
	if err != nil {
		t.Fatalf("create goose provider: %v", err)
	}
	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	return rwdb.New(db)
}

func newAuthedHandler(t *testing.T) (http.Handler, *topologysvc.Service, string) {
	t.Helper()

	db := newTestDB(t)
	t.Cleanup(func() { _ = db.Close() })

	authService := authsvc.New(db)
	userService := usersvc.NewService(db)
	topologyService := topologysvc.NewService(nil)

	user, err := authService.Register(t.Context(), authsvc.RegisterRequest{
		Username: "alice",
		Password: "secret",
	})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}

	if err := userService.GrantAccess(t.Context(), user.ID, usersvc.AppAccess{
		CollectorName: "collector-a",
		AppName:       "app-a",
	}); err != nil {
		t.Fatalf("grant access: %v", err)
	}

	token, err := authService.CreateSession(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/topology/", http.StripPrefix("/topology", InitMux(topologyService, userService)))
	handler := authmw.BuildAuthMiddleware(authService, userService)(mux)
	return handler, topologyService, token
}

func TestTopologyEventsRequiresAuthentication(t *testing.T) {
	handler, _, _ := newAuthedHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/topology/events", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestTopologyEventsStreamsOnlyAuthorizedEvents(t *testing.T) {
	handler, topologyService, token := newAuthedHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/topology/events", nil)
	req.AddCookie(&http.Cookie{Name: "tracer_session", Value: token})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		handler.ServeHTTP(rr, req)
	}()

	time.Sleep(30 * time.Millisecond)
	unauthorized, err := json.Marshal(types.HealthEvent{Time: time.Now(), App: "app-b", Status: types.HealthStatusDown})
	if err != nil {
		t.Fatalf("marshal unauthorized event: %v", err)
	}
	authorized, err := json.Marshal(types.HealthEvent{Time: time.Now(), App: "app-a", Status: types.HealthStatusUp})
	if err != nil {
		t.Fatalf("marshal authorized event: %v", err)
	}
	topologyService.Publish(topologysvc.Message{
		Envelope: &types.Envelope{Type: types.EventTypeHealth, Collector: "collector-b", Timestamp: time.Now(), Data: unauthorized},
	})
	topologyService.Publish(topologysvc.Message{
		Envelope: &types.Envelope{Type: types.EventTypeHealth, Collector: "collector-a", Timestamp: time.Now(), Data: authorized},
	})
	time.Sleep(30 * time.Millisecond)
	cancel()
	wg.Wait()

	body := rr.Body.String()
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected text/event-stream content type, got %q", ct)
	}
	if !strings.Contains(body, `"collector":"collector-a"`) {
		t.Fatalf("expected authorized event in body, got %q", body)
	}
	if strings.Contains(body, `"collector":"collector-b"`) {
		t.Fatalf("unexpected unauthorized event in body, got %q", body)
	}
	if !strings.Contains(body, "event: tick") {
		t.Fatalf("expected initial tick in body, got %q", body)
	}
}
