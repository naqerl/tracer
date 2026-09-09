package supervisor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeComponent records start/stop and blocks until its context is cancelled.
type fakeComponent struct {
	id        string
	sig       string
	started   *int32
	stopped   *int32
	runCalled *int32
}

func (f fakeComponent) ID() string        { return f.id }
func (f fakeComponent) Signature() string { return f.sig }
func (f fakeComponent) Run(ctx context.Context) {
	atomic.AddInt32(f.started, 1)
	atomic.AddInt32(f.runCalled, 1)
	<-ctx.Done()
	atomic.AddInt32(f.stopped, 1)
}

func newFake(id, sig string) fakeComponent {
	return fakeComponent{id: id, sig: sig, started: new(int32), stopped: new(int32), runCalled: new(int32)}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestApplyStartsNewComponents(t *testing.T) {
	s := New()
	a := newFake("app:a", "s1")
	b := newFake("app:b", "s1")

	s.Apply(t.Context(), []Component{a, b})

	waitFor(t, func() bool { return atomic.LoadInt32(a.started) == 1 && atomic.LoadInt32(b.started) == 1 })
	if got := atomic.LoadInt32(a.runCalled); got != 1 {
		t.Fatalf("a should run once, ran %d", got)
	}
}

func TestApplyKeepsUnchangedRunning(t *testing.T) {
	s := New()
	a := newFake("app:a", "s1")
	s.Apply(t.Context(), []Component{a})
	waitFor(t, func() bool { return atomic.LoadInt32(a.started) == 1 })

	// Re-apply with the same component: must not restart.
	s.Apply(t.Context(), []Component{a})
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(a.runCalled); got != 1 {
		t.Fatalf("a should not have restarted, run count %d", got)
	}
	if got := atomic.LoadInt32(a.stopped); got != 0 {
		t.Fatalf("a should not have stopped, stop count %d", got)
	}
}

func TestApplyRestartsChangedSignature(t *testing.T) {
	s := New()
	a1 := newFake("app:a", "s1")
	s.Apply(t.Context(), []Component{a1})
	waitFor(t, func() bool { return atomic.LoadInt32(a1.started) == 1 })

	a2 := newFake("app:a", "s2")
	s.Apply(t.Context(), []Component{a2})
	waitFor(t, func() bool { return atomic.LoadInt32(a2.started) == 1 })
	waitFor(t, func() bool { return atomic.LoadInt32(a1.stopped) == 1 })
	if got := atomic.LoadInt32(a2.runCalled); got != 1 {
		t.Fatalf("a2 should run once, ran %d", got)
	}
}

func TestApplyStopsRemovedComponents(t *testing.T) {
	s := New()
	a := newFake("app:a", "s1")
	b := newFake("app:b", "s1")
	s.Apply(t.Context(), []Component{a, b})
	waitFor(t, func() bool { return atomic.LoadInt32(b.started) == 1 })

	s.Apply(t.Context(), []Component{a})
	waitFor(t, func() bool { return atomic.LoadInt32(b.stopped) == 1 })
	if got := atomic.LoadInt32(a.stopped); got != 0 {
		t.Fatalf("a should keep running, stop count %d", got)
	}
}

func TestStopCancelsAll(t *testing.T) {
	s := New()
	a := newFake("app:a", "s1")
	s.Apply(t.Context(), []Component{a})
	waitFor(t, func() bool { return atomic.LoadInt32(a.started) == 1 })

	s.Stop()
	waitFor(t, func() bool { return atomic.LoadInt32(a.stopped) == 1 })

	if len(s.runners) != 0 {
		t.Fatalf("runners should be empty after Stop, got %d", len(s.runners))
	}
}

func TestConcurrentApply(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := newFake("app:a", "s1")
			s.Apply(t.Context(), []Component{c})
		}(i)
	}
	wg.Wait()
	// Exactly one instance of "app:a" must be running despite the races.
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.runners) == 1
	})
}
