package update

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"tracer/libs/shared/runtime"
)

// Updater orchestrates the stages of a self-update (download, build, verify,
// install, restart) and reports progress via a Report.  Each stage is
// delegated to an Installer, and progress events are emitted through a
// ProgressProxy that wraps the Installer.
type Updater struct {
	slug  runtime.Slug
	runFn func(chan<- ProgressEvent) error // injectable for testing; defaults to u.run
}

// NewUpdater creates an Updater for the given service slug.
func NewUpdater(slug runtime.Slug) *Updater {
	u := &Updater{slug: slug}
	u.runFn = u.run
	return u
}

// Run executes the self-update in a goroutine. It returns a Report whose
// Events channel emits a ProgressEvent for each stage transition. Call
// Wait() on the Report to block until the update completes and get any error.
func (u *Updater) Run() *Report {
	events := make(chan ProgressEvent, 8)
	done := make(chan struct{})
	r := &Report{Events: events, done: done}

	go func() {
		defer close(events)
		defer func() {
			if p := recover(); p != nil {
				r.setErr(fmt.Errorf("update panicked: %v", p))
			}
		}()
		err := u.runFn(events)
		r.setErr(err)
	}()

	return r
}

// run performs the update synchronously, sending events to the given channel.
// It creates a proxy-wrapped realInstaller and calls each stage in order.
// The proxy writes progress events directly to the report's events channel.
func (u *Updater) run(events chan<- ProgressEvent) error {
	srcDir := "/tmp/tracer-update"
	os.RemoveAll(srcDir)

	proxy := NewProgressProxy(realInstaller{}, events)
	ctx := context.Background()

	fmt.Println("Updating tracer from source...")
	fmt.Printf("Repository: %s\n", repoURL)

	// 1. Download
	if err := proxy.Download(ctx, srcDir); err != nil {
		return err
	}

	// 2. Build
	if err := proxy.Build(ctx, srcDir, u.slug); err != nil {
		return err
	}

	// 3. Verify
	if err := proxy.Verify(ctx, binaryPath(srcDir, u.slug)); err != nil {
		os.RemoveAll(srcDir)
		return err
	}

	// 4. Install
	mgr := runtime.NewManager(u.slug)
	if err := proxy.Install(ctx, binaryPath(srcDir, u.slug), destPath(mgr, u.slug)); err != nil {
		os.RemoveAll(srcDir)
		return err
	}
	os.RemoveAll(srcDir)

	// 5. Restart
	if err := proxy.Restart(ctx, mgr); err != nil {
		slog.Warn("Post-update restart failed", "error", err)
	} else {
		events <- ProgressEvent{Stage: StageComplete, Status: StatusDone}
	}

	return nil
}

func errUnknownSlug(slug runtime.Slug) error {
	return fmt.Errorf("unknown service slug: %s", slug)
}
