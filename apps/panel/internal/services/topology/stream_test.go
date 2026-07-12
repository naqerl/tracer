package topology

import (
	"testing"
	"time"

	"tracer/libs/types"
)

func TestStreamPublishesToSubscribers(t *testing.T) {
	svc := NewService(nil)
	ch, unsubscribe := svc.Subscribe()
	defer unsubscribe()

	want := Message{
		Register: &types.RegisterEvent{Type: types.EventTypeRegister, Collector: "alpha", Apps: []string{"api"}},
	}
	svc.Publish(want)

	select {
	case got := <-ch:
		if got.Register == nil {
			t.Fatalf("unexpected event: %+v", got)
		}
		if got.Register.Type != want.Register.Type || got.Register.Collector != want.Register.Collector || len(got.Register.Apps) != 1 || got.Register.Apps[0] != "api" {
			t.Fatalf("unexpected event: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published event")
	}
}

func TestStreamUnsubscribeStopsDelivery(t *testing.T) {
	svc := NewService(nil)
	ch, unsubscribe := svc.Subscribe()
	unsubscribe()

	svc.Publish(Message{Register: &types.RegisterEvent{Type: types.EventTypeRegister, Collector: "alpha"}})

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for closed channel")
	}
}
