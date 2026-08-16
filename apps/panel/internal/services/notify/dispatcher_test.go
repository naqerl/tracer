package notify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	authsvc "tracer/apps/panel/internal/services/auth"
	usersvc "tracer/apps/panel/internal/services/user"
	"tracer/apps/panel/internal/testutil"

	"tracer/apps/panel/internal/domain"
)

// fakeNotifier records every notification it receives.
type fakeNotifier struct {
	mu       sync.Mutex
	received []Notification
}

func (f *fakeNotifier) Type() string { return "test" }
func (f *fakeNotifier) Name() string { return "fake" }
func (f *fakeNotifier) Notify(n Notification) error {
	f.mu.Lock()
	f.received = append(f.received, n)
	f.mu.Unlock()
	return nil
}

// startDispatcher builds a dispatcher backed by a real panel DB with one
// user, one notifier subscribed to prod/api, and starts its loop.
func startDispatcher(t *testing.T) (*Dispatcher, chan Notification, *fakeNotifier, context.CancelFunc) {
	t.Helper()

	db := testutil.NewTestDB(t)
	userSvc := usersvc.NewService(db)
	authSvc := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	registered, err := authSvc.Register(t.Context(), authsvc.RegisterRequest{
		Username: "alice",
		Password: "secret",
		IsAdmin:  true,
	})
	if err != nil {
		t.Fatalf("register user: %v", err)
	}
	user, err := userSvc.FindByID(t.Context(), domain.System{}, registered.ID)
	if err != nil {
		t.Fatalf("find user: %v", err)
	}

	collected := &fakeNotifier{}
	factories := map[string]FromRawJSON{
		"test": func(_, _ string) (Notifier, error) { return collected, nil },
	}

	service := New(db, userSvc, factories)
	if err := service.Add(t.Context(), domain.User{ID: user.ID, Role: domain.RoleAdmin}, AddNotifierRequest{
		UserID: user.ID,
		Name:   "fake",
		Type:   "test",
		Config: "{}",
	}); err != nil {
		t.Fatalf("add notifier: %v", err)
	}
	if err := service.SetSubscriptions(t.Context(), domain.User{ID: user.ID, Role: domain.RoleAdmin}, 1, []Subscription{
		{CollectorName: "prod", AppName: "api"},
	}); err != nil {
		t.Fatalf("set subscriptions: %v", err)
	}

	ch := make(chan Notification, 4)
	dispatcher := NewDispatcher(db, factories, ch, userSvc)

	ctx, cancel := context.WithCancel(t.Context())
	go dispatcher.Start(ctx)
	t.Cleanup(cancel)

	return dispatcher, ch, collected, cancel
}

// waitForNotification polls the fake until a notification of the given type
// arrives (the fake also records the welcome notification sent by Service.Add).
func waitForNotification(t *testing.T, collected *fakeNotifier, typ NotificationType) Notification {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		collected.mu.Lock()
		var found Notification
		for _, n := range collected.received {
			if n.Type == typ {
				found = n
				break
			}
		}
		collected.mu.Unlock()

		if found.Type != "" {
			return found
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s notification", typ)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDispatcherPassesStructuredNotification(t *testing.T) {
	_, ch, collected, _ := startDispatcher(t)

	ch <- Notification{
		Type:          TypeAlert,
		CollectorName: "prod",
		AppName:       "api",
		Payload: LogEntry{
			Level:     "error",
			Message:   "boom: connection refused",
			GroupID:   7,
			LogID:     99,
			CreatedAt: time.Now(),
			LogURL:    "https://panel/panel/collectors/prod/apps/api/logs/99",
		},
	}

	n := waitForNotification(t, collected, TypeAlert)
	if n.CollectorName != "prod" || n.AppName != "api" {
		t.Errorf("expected prod/api, got %s/%s", n.CollectorName, n.AppName)
	}
	log, ok := n.Payload.(LogEntry)
	if !ok {
		t.Fatalf("expected LogEntry payload, got %T", n.Payload)
	}
	if log.GroupID != 7 || log.LogID != 99 || log.Message != "boom: connection refused" || log.LogURL == "" {
		t.Errorf("log entry not preserved: %+v", log)
	}
}

func TestDispatcherPassesPlainPayload(t *testing.T) {
	_, ch, collected, _ := startDispatcher(t)

	ch <- Notification{
		Type:          TypeLifecycle,
		CollectorName: "prod",
		AppName:       "api",
		Payload:       "restart",
	}

	n := waitForNotification(t, collected, TypeLifecycle)
	if n.Payload != "restart" {
		t.Errorf("expected plain payload to pass through, got %v", n.Payload)
	}
}

// TestNotifyServiceAccessDenial covers every access-checked method with
// non-owner and unauthenticated actors: each must return ErrAccessDenied.
func TestNotifyServiceAccessDenial(t *testing.T) {
	db := testutil.NewTestDB(t)
	userSvc := usersvc.NewService(db)
	authSvc := authsvc.New(db, testutil.TestKeySecret(), bcrypt.MinCost)

	alice, err := authSvc.Register(t.Context(), authsvc.RegisterRequest{Username: "alice", Password: "secret", IsAdmin: false})
	if err != nil {
		t.Fatalf("register alice: %v", err)
	}
	bob, err := authSvc.Register(t.Context(), authsvc.RegisterRequest{Username: "bob", Password: "secret", IsAdmin: false})
	if err != nil {
		t.Fatalf("register bob: %v", err)
	}

	collected := &fakeNotifier{}
	service := New(db, userSvc, map[string]FromRawJSON{
		"test": func(_, _ string) (Notifier, error) { return collected, nil },
	})
	if err := service.Add(t.Context(), domain.User{ID: alice.ID}, AddNotifierRequest{
		UserID: alice.ID,
		Name:   "alice-notifier",
		Type:   "test",
		Config: "{}",
	}); err != nil {
		t.Fatalf("add notifier: %v", err)
	}

	nonOwner := domain.User{ID: bob.ID}
	unauthenticated := domain.Unauthenticated{}

	// Non-owner gets ErrAccessDenied on every method.
	if _, err := service.GetSetup(t.Context(), nonOwner, 1); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("GetSetup non-owner: expected ErrAccessDenied, got %v", err)
	}
	if _, err := service.GetSubscriptions(t.Context(), nonOwner, 1); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("GetSubscriptions non-owner: expected ErrAccessDenied, got %v", err)
	}
	if err := service.SetSubscriptions(t.Context(), nonOwner, 1, []Subscription{{CollectorName: "prod", AppName: "api"}}); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("SetSubscriptions non-owner: expected ErrAccessDenied, got %v", err)
	}
	if err := service.Delete(t.Context(), nonOwner, 1); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("Delete non-owner: expected ErrAccessDenied, got %v", err)
	}
	if err := service.Toggle(t.Context(), nonOwner, 1, true); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("Toggle non-owner: expected ErrAccessDenied, got %v", err)
	}

	// Unauthenticated gets ErrAccessDenied too (never ErrUnexpectedActor).
	if _, err := service.GetSetup(t.Context(), unauthenticated, 1); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("GetSetup unauthenticated: expected ErrAccessDenied, got %v", err)
	}
	if _, err := service.GetSubscriptions(t.Context(), unauthenticated, 1); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("GetSubscriptions unauthenticated: expected ErrAccessDenied, got %v", err)
	}
	if err := service.SetSubscriptions(t.Context(), unauthenticated, 1, []Subscription{{CollectorName: "prod", AppName: "api"}}); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("SetSubscriptions unauthenticated: expected ErrAccessDenied, got %v", err)
	}
	if err := service.Delete(t.Context(), unauthenticated, 1); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("Delete unauthenticated: expected ErrAccessDenied, got %v", err)
	}
	if err := service.Toggle(t.Context(), unauthenticated, 1, true); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("Toggle unauthenticated: expected ErrAccessDenied, got %v", err)
	}
	if _, err := service.List(t.Context(), unauthenticated); !errors.Is(err, ErrAccessDenied) {
		t.Errorf("List unauthenticated: expected ErrAccessDenied, got %v", err)
	}

	// Add for another user's id is denied for a non-admin.
	err = service.Add(t.Context(), nonOwner, AddNotifierRequest{
		UserID: alice.ID,
		Name:   "steal",
		Type:   "test",
		Config: "{}",
	})
	if !errors.Is(err, ErrAccessDenied) {
		t.Errorf("Add cross-user: expected ErrAccessDenied, got %v", err)
	}

	// The owner still has access after all the denials.
	if _, err := service.GetSetup(t.Context(), domain.User{ID: alice.ID}, 1); err != nil {
		t.Errorf("GetSetup owner: expected success, got %v", err)
	}
}
