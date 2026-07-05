package updatex

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"tracer/libs/types"
)

// ── Report tests ──────────────────────────────────────────────────────────────

func TestReportWait_HappyPath(t *testing.T) {
	events := make(chan ProgressEvent, 8)
	done := make(chan struct{})
	r := &Report{Events: events, done: done}

	go func() {
		events <- ProgressEvent{Stage: "test", Status: types.ProgressInProgress}
		events <- ProgressEvent{Stage: "test", Status: types.ProgressDone}
		close(events)
		r.setErr(nil)
	}()

	err := r.Wait()
	if err != nil {
		t.Errorf("Wait() returned error: %v", err)
	}
}

func TestReportWait_ErrorPath(t *testing.T) {
	events := make(chan ProgressEvent, 8)
	done := make(chan struct{})
	r := &Report{Events: events, done: done}

	expectedErr := errors.New("update failed")
	go func() {
		close(events)
		r.setErr(expectedErr)
	}()

	err := r.Wait()
	if !errors.Is(err, expectedErr) {
		t.Errorf("Wait() returned %v, want %v", err, expectedErr)
	}
}

func TestReportWait_BlocksUntilDone(t *testing.T) {
	events := make(chan ProgressEvent, 8)
	done := make(chan struct{})
	r := &Report{Events: events, done: done}

	doneCh := make(chan struct{})
	go func() {
		r.Wait()
		close(doneCh)
	}()

	// Give the goroutine a moment to block on Wait
	select {
	case <-doneCh:
		t.Fatal("Wait() returned before setErr was called")
	default:
	}

	// Now signal completion
	r.setErr(nil)

	// Should unblock
	<-doneCh
}

// ── Report.setErr idempotency tests ───────────────────────────────────────────

func TestSetErr_Idempotency(t *testing.T) {
	events := make(chan ProgressEvent)
	done := make(chan struct{})
	r := &Report{Events: events, done: done}

	firstErr := errors.New("first error")
	secondErr := errors.New("second error")

	r.setErr(firstErr)
	r.setErr(secondErr)

	// done must be closed after first setErr
	<-done

	if r.err != firstErr {
		t.Errorf("setErr: got %v, want %v", r.err, firstErr)
	}
}

func TestSetErr_IdempotencyNilAfterError(t *testing.T) {
	events := make(chan ProgressEvent)
	done := make(chan struct{})
	r := &Report{Events: events, done: done}

	firstErr := errors.New("real error")

	r.setErr(firstErr)
	r.setErr(nil) // second call with nil should be ignored

	<-done

	if r.err != firstErr {
		t.Errorf("setErr: got %v, want %v (nil should not overwrite)", r.err, firstErr)
	}
}

func TestSetErr_IdempotencyErrorAfterNil(t *testing.T) {
	events := make(chan ProgressEvent)
	done := make(chan struct{})
	r := &Report{Events: events, done: done}

	r.setErr(nil)                        // success
	r.setErr(errors.New("late failure")) // should be ignored

	<-done

	if r.err != nil {
		t.Errorf("setErr: got %v, want nil (late error should not overwrite success)", r.err)
	}
}

// ── Updater.Run() tests with injectable runFn ─────────────────────────────────

func TestUpdaterRun_ReturnsReport(t *testing.T) {
	u := &Updater{
		slug: "test-slug",
		runFn: func(events chan<- ProgressEvent) error {
			events <- ProgressEvent{Stage: "test", Status: types.ProgressDone}
			return nil
		},
	}

	r := u.Run()
	if r == nil {
		t.Fatal("Run() returned nil")
	}
	if r.Events == nil {
		t.Error("Run() returned Report with nil Events channel")
	}

	// Should receive the event we sent
	evt, ok := <-r.Events
	if !ok {
		t.Fatal("Events channel closed before receiving any events")
	}
	if evt.Stage != "test" {
		t.Errorf("Got stage %q, want %q", evt.Stage, "test")
	}
	if evt.Status != types.ProgressDone {
		t.Errorf("Got status %q, want %q", evt.Status, types.ProgressDone)
	}

	// Wait should succeed
	err := r.Wait()
	if err != nil {
		t.Errorf("Wait() returned error: %v", err)
	}
}

func TestUpdaterRun_ErrorPropagation(t *testing.T) {
	expectedErr := errors.New("build failed")
	u := &Updater{
		slug: "test-slug",
		runFn: func(events chan<- ProgressEvent) error {
			events <- ProgressEvent{Stage: "build", Status: types.ProgressFailed, Message: "build failed"}
			return expectedErr
		},
	}

	r := u.Run()

	// Drain events
	for range r.Events {
	}

	err := r.Wait()
	if !errors.Is(err, expectedErr) {
		t.Errorf("Wait() returned %v, want %v", err, expectedErr)
	}
}

func TestUpdaterRun_ClosesEventsOnCompletion(t *testing.T) {
	u := &Updater{
		slug: "test-slug",
		runFn: func(events chan<- ProgressEvent) error {
			events <- ProgressEvent{Stage: "done", Status: types.ProgressDone}
			return nil
		},
	}

	r := u.Run()

	// Read all events - channel should be closed by Run() after runFn returns
	count := 0
	for range r.Events {
		count++
	}
	if count != 1 {
		t.Errorf("Got %d events, want 1", count)
	}

	// Wait should succeed
	if err := r.Wait(); err != nil {
		t.Errorf("Wait() returned error: %v", err)
	}
}

func TestUpdaterRun_PanicRecovery(t *testing.T) {
	u := &Updater{
		slug: "test-slug",
		runFn: func(events chan<- ProgressEvent) error {
			events <- ProgressEvent{Stage: "boom", Status: types.ProgressInProgress}
			panic("something went terribly wrong")
		},
	}

	r := u.Run()

	// Drain events
	for range r.Events {
	}

	err := r.Wait()
	if err == nil {
		t.Fatal("Wait() should return error after panic")
	}
	if !errors.Is(err, errors.New("update panicked: something went terribly wrong")) {
		// Compare by string since errors.Is checks chain
		if err.Error() != "update panicked: something went terribly wrong" {
			t.Errorf("Wait() returned %v, want panic error message", err)
		}
	}
}

// ── ProgressEvent ordering ────────────────────────────────────────────────────

func TestProgressEvent_Ordering(t *testing.T) {
	expectedStages := []string{
		"download",
		"build",
		"verify",
		"install",
		"restart",
		"complete",
	}

	u := &Updater{
		slug: "test-slug",
		runFn: func(events chan<- ProgressEvent) error {
			for _, stage := range expectedStages {
				events <- ProgressEvent{Stage: stage, Status: types.ProgressDone}
			}
			return nil
		},
	}

	r := u.Run()

	var gotStages []string
	for evt := range r.Events {
		gotStages = append(gotStages, evt.Stage)
	}

	if len(gotStages) != len(expectedStages) {
		t.Fatalf("Got %d events, want %d\ngot:  %v\nwant: %v",
			len(gotStages), len(expectedStages), gotStages, expectedStages)
	}
	for i, stage := range expectedStages {
		if gotStages[i] != stage {
			t.Errorf("Event %d: got %q, want %q", i, gotStages[i], stage)
		}
	}
}

func TestProgressEvent_OrderingWithFailures(t *testing.T) {
	// Simulate a build failure: download succeeds, build fails, no further stages
	u := &Updater{
		slug: "test-slug",
		runFn: func(events chan<- ProgressEvent) error {
			events <- ProgressEvent{Stage: "download", Status: types.ProgressInProgress, Message: "Downloading..."}
			events <- ProgressEvent{Stage: "download", Status: types.ProgressDone}
			events <- ProgressEvent{Stage: "build", Status: types.ProgressInProgress, Message: "Building..."}
			events <- ProgressEvent{Stage: "build", Status: types.ProgressFailed, Message: "compilation error"}
			return errors.New("compilation error")
		},
	}

	r := u.Run()

	var events []ProgressEvent
	for evt := range r.Events {
		events = append(events, evt)
	}

	if len(events) != 4 {
		t.Fatalf("Got %d events, want 4", len(events))
	}

	// Verify the sequence
	checks := []struct {
		index  int
		stage  string
		status types.ProgressStatus
	}{
		{0, "download", types.ProgressInProgress},
		{1, "download", types.ProgressDone},
		{2, "build", types.ProgressInProgress},
		{3, "build", types.ProgressFailed},
	}
	for _, c := range checks {
		if events[c.index].Stage != c.stage {
			t.Errorf("Event %d stage: got %q, want %q", c.index, events[c.index].Stage, c.stage)
		}
		if events[c.index].Status != c.status {
			t.Errorf("Event %d status: got %q, want %q", c.index, events[c.index].Status, c.status)
		}
	}

	err := r.Wait()
	if err == nil {
		t.Error("Wait() should return error after build failure")
	}
}

// ── NewUpdater default wiring ─────────────────────────────────────────────────

func TestNewUpdater_SetsDefaultRunFn(t *testing.T) {
	u := NewUpdater("test-slug")
	if u == nil {
		t.Fatal("NewUpdater() returned nil")
	}
	if u.slug != "test-slug" {
		t.Errorf("slug = %q, want %q", u.slug, "test-slug")
	}
	if u.runFn == nil {
		t.Error("runFn should not be nil after NewUpdater")
	}
}

// ── VerifyBinary with temp binary ─────────────────────────────────────────────

func TestVerifyBinary_ValidBinary(t *testing.T) {
	// Create a temp directory for the fake binary
	td := t.TempDir()
	binaryPath := filepath.Join(td, "test-binary")

	// Create a shell script that responds to --version
	script := `#!/bin/sh
echo "tracer-test v1.0.0"
`
	if err := os.WriteFile(binaryPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	err := VerifyBinary(binaryPath)
	if err != nil {
		t.Errorf("VerifyBinary() returned error: %v", err)
	}
}

func TestVerifyBinary_EmptyOutput(t *testing.T) {
	td := t.TempDir()
	binaryPath := filepath.Join(td, "silent-binary")

	// A script that outputs nothing to stdout
	script := `#!/bin/sh
exit 0
`
	if err := os.WriteFile(binaryPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	err := VerifyBinary(binaryPath)
	if err == nil {
		t.Error("VerifyBinary() should return error for empty output")
	}
}

func TestVerifyBinary_NonExistentPath(t *testing.T) {
	err := VerifyBinary("/nonexistent/binary")
	if err == nil {
		t.Error("VerifyBinary() should return error for non-existent binary")
	}
}

func TestVerifyBinary_NonExecutable(t *testing.T) {
	td := t.TempDir()
	binaryPath := filepath.Join(td, "not-executable")

	if err := os.WriteFile(binaryPath, []byte("not a binary"), 0644); err != nil {
		t.Fatal(err)
	}

	err := VerifyBinary(binaryPath)
	if err == nil {
		t.Error("VerifyBinary() should return error for non-executable file")
	}
}

// ── DetectArchitecture tests ──────────────────────────────────────────────────

func TestDetectArchitecture_ReturnsString(t *testing.T) {
	arch := DetectArchitecture()
	if arch == "" {
		t.Error("DetectArchitecture() returned empty string")
	}
}

// ── Helper: ProgressEvent convenience ─────────────────────────────────────────

func TestProgressEvent_Fields(t *testing.T) {
	evt := ProgressEvent{
		Stage:   "download",
		Status:  types.ProgressInProgress,
		Message: "Downloading...",
	}
	if evt.Stage != "download" {
		t.Errorf("Stage = %q, want %q", evt.Stage, "download")
	}
	if evt.Status != types.ProgressInProgress {
		t.Errorf("Status = %q, want %q", evt.Status, types.ProgressInProgress)
	}
	if evt.Message != "Downloading..." {
		t.Errorf("Message = %q, want %q", evt.Message, "Downloading...")
	}
}

// ── Verify the refactoring didn't break NewUpdater().Run() contract ───────────

func TestNewUpdaterRun_ReturnsReportWithEventsChannel(t *testing.T) {
	// Use a slug that doesn't match real slugs so run() returns errUnknownSlug quickly
	u := NewUpdater("non-existent-slug")
	r := u.Run()
	if r == nil {
		t.Fatal("Run() returned nil")
	}
	if r.Events == nil {
		t.Error("Run() returned Report with nil Events channel")
	}

	// Drain events (should be exactly one: the error)
	count := 0
	for range r.Events {
		count++
	}
	t.Logf("Received %d events from real run() with invalid slug", count)

	err := r.Wait()
	if err == nil {
		t.Error("Wait() should return error for unknown slug")
	}
}
