package topology

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	authsvc "tracer/apps/panel/internal/services/auth"
	topologysvc "tracer/apps/panel/internal/services/topology"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/testutil"
	authmw "tracer/apps/panel/web/middleware/auth"
	"tracer/libs/types"
)

func newAuthedHandler(t *testing.T) (http.Handler, *topologysvc.Service, string) {
	t.Helper()

	db := testutil.NewTestDB(t)

	// Low bcrypt cost: cost 12 is ~250ms per hash, too slow for the suite.
	authService := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)
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
	ctx, cancel := context.WithCancel(t.Context())
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
	topologyService.Publish(topologysvc.Message{
		Envelope: &types.Envelope{Type: types.EventTypeHealth, Collector: "collector-b", Time: time.Now(), Data: types.HealthEvent{Time: time.Now(), App: "app-b", Status: types.HealthStatusDown}},
	})
	topologyService.Publish(topologysvc.Message{
		Envelope: &types.Envelope{Type: types.EventTypeHealth, Collector: "collector-a", Time: time.Now(), Data: types.HealthEvent{Time: time.Now(), App: "app-a", Status: types.HealthStatusUp}},
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

func TestToGraphSnapshotAddsElementIDs(t *testing.T) {
	snapshot := topologysvc.Snapshot{
		Collectors: []topologysvc.Collector{
			{
				Name: "prod",
				Apps: []topologysvc.App{
					{Name: "api", HasHealth: true},
					{Name: "web/app", HasHealth: false},
				},
			},
		},
	}

	got := toGraphSnapshot(snapshot)

	if got.Panel.ID != "panel-root" {
		t.Errorf("panel id = %q, want %q", got.Panel.ID, "panel-root")
	}
	if len(got.Collectors) != 1 {
		t.Fatalf("collectors = %d, want 1", len(got.Collectors))
	}
	c := got.Collectors[0]
	if c.ID != "collector-prod-0" {
		t.Errorf("collector id = %q, want %q", c.ID, "collector-prod-0")
	}
	if c.Name != "prod" {
		t.Errorf("collector name = %q, want %q", c.Name, "prod")
	}
	if len(c.Apps) != 2 {
		t.Fatalf("apps = %d, want 2", len(c.Apps))
	}
	wantApps := []graphApp{
		{ID: "app-prod-api-0", Name: "api", HasHealth: true},
		{ID: "app-prod-web-app-1", Name: "web/app", HasHealth: false},
	}
	for i, want := range wantApps {
		if c.Apps[i] != want {
			t.Errorf("app %d = %+v, want %+v", i, c.Apps[i], want)
		}
	}
}

func TestToGraphSnapshotCollisionFreesIds(t *testing.T) {
	// "a/b" and "a-b" sanitize to the same id; the per-row index must keep
	// every collector and app id distinct.
	snapshot := topologysvc.Snapshot{
		Collectors: []topologysvc.Collector{
			{Name: "a/b", Apps: []topologysvc.App{{Name: "x"}}},
			{Name: "a-b", Apps: []topologysvc.App{{Name: "x"}}},
		},
	}

	got := toGraphSnapshot(snapshot)

	if len(got.Collectors) != 2 {
		t.Fatalf("collectors = %d, want 2", len(got.Collectors))
	}
	if got.Collectors[0].ID == got.Collectors[1].ID {
		t.Fatalf("collector ids collide: %q", got.Collectors[0].ID)
	}
	if got.Collectors[0].Apps[0].ID == got.Collectors[1].Apps[0].ID {
		t.Fatalf("app ids collide: %q", got.Collectors[0].Apps[0].ID)
	}
}

func TestDecodeData(t *testing.T) {
	env := types.Envelope{
		Type: types.EventTypeHealth,
		Time: time.Now(),
		Data: map[string]any{
			"time":   "2025-01-15T10:00:00Z",
			"app":    "myapp",
			"status": "up",
		},
	}

	var evt types.HealthEvent
	if err := env.DecodeData(&evt); err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if evt.App != "myapp" {
		t.Fatalf("app: got %q, want %q", evt.App, "myapp")
	}
	if evt.Status != types.HealthStatusUp {
		t.Fatalf("status: got %q, want %q", evt.Status, types.HealthStatusUp)
	}
}

func TestDecodeDataFromTyped(t *testing.T) {
	// Simulate the path where Data is already a typed event (tests, direct assignment).
	inner := types.HealthEvent{Time: time.Now(), App: "srv", Status: types.HealthStatusDown}
	env := types.Envelope{
		Type:      types.EventTypeHealth,
		Time:      time.Now(),
		Collector: "c1",
		Data:      inner,
	}

	var got types.HealthEvent
	if err := env.DecodeData(&got); err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if got.App != "srv" {
		t.Fatalf("app: got %q, want %q", got.App, "srv")
	}
	if got.Status != types.HealthStatusDown {
		t.Fatalf("status: got %q, want %q", got.Status, types.HealthStatusDown)
	}
}

func TestDecodeDataInvalid(t *testing.T) {
	env := types.Envelope{
		Type: types.EventTypeHealth,
		Time: time.Now(),
		Data: 42,
	}
	var evt types.HealthEvent
	if err := env.DecodeData(&evt); err == nil {
		t.Fatal("expected error for int data, got nil")
	}
}
