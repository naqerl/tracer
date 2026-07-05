package update

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	goRuntime "runtime"

	"tracer/libs/shared/runtime"
	"tracer/libs/types"
)

// Updater performs the stages of a self-update (download, build, verify,
// install, restart) and reports progress via a Report.
type Updater struct {
	slug runtime.Slug
}

// NewUpdater creates an Updater for the given service slug.
func NewUpdater(slug runtime.Slug) *Updater {
	return &Updater{slug: slug}
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
		err := u.run(events)
		r.setErr(err)
	}()

	return r
}

// run performs the update synchronously, sending events to the given channel.
// It is called in a goroutine by Run().
func (u *Updater) run(events chan<- ProgressEvent) error {
	srcDir := "/tmp/tracer-update"
	fmt.Println("Updating tracer from source...")
	fmt.Printf("Repository: %s\n", repoURL)

	// ----------------
	// 1. Download
	// ----------------
	events <- ProgressEvent{Stage: "download", Status: types.ProgressInProgress, Message: "Downloading source snapshot..."}
	os.RemoveAll(srcDir)

	if err := downloadSnapshot(srcDir); err != nil {
		events <- ProgressEvent{Stage: "download", Status: types.ProgressFailed, Message: err.Error()}
		return err
	}
	events <- ProgressEvent{Stage: "download", Status: types.ProgressDone}

	// ----------------
	// 2. Build
	// ----------------
	events <- ProgressEvent{Stage: "build", Status: types.ProgressInProgress, Message: "Building from source..."}

	origDir, err := os.Getwd()
	if err != nil {
		os.RemoveAll(srcDir)
		return err
	}
	if err := os.Chdir(srcDir); err != nil {
		os.RemoveAll(srcDir)
		return err
	}
	defer os.Chdir(origDir)

	var buildTarget string
	switch u.slug {
	case runtime.SlugCollector:
		buildTarget = "build-collector"
	case runtime.SlugPanel:
		buildTarget = "build-panel"
	default:
		return errUnknownSlug(u.slug)
	}

	cmd := exec.Command("make", buildTarget)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		events <- ProgressEvent{Stage: "build", Status: types.ProgressFailed, Message: err.Error()}
		return err
	}
	events <- ProgressEvent{Stage: "build", Status: types.ProgressDone}

	// ----------------
	// 3. Verify
	// ----------------
	events <- ProgressEvent{Stage: "verify", Status: types.ProgressInProgress, Message: "Verifying binary..."}

	binaryPath := filepath.Join(srcDir, "bin", string(u.slug))
	if err := VerifyBinary(binaryPath); err != nil {
		os.RemoveAll(srcDir)
		events <- ProgressEvent{Stage: "verify", Status: types.ProgressFailed, Message: err.Error()}
		return err
	}
	events <- ProgressEvent{Stage: "verify", Status: types.ProgressDone}

	// ----------------
	// 4. Install
	// ----------------
	mgr := runtime.NewManager(u.slug)
	destPath := filepath.Join(mgr.BinaryDir(), string(u.slug))

	events <- ProgressEvent{Stage: "install", Status: types.ProgressInProgress, Message: "Installing binary..."}

	backupPath := destPath + ".bak"
	if _, err := os.Stat(destPath); err == nil {
		if err := copyFile(destPath, backupPath); err != nil {
			os.RemoveAll(srcDir)
			events <- ProgressEvent{Stage: "install", Status: types.ProgressFailed, Message: err.Error()}
			return err
		}
		defer func() {
			if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
				slog.Warn("Could not remove backup", "path", backupPath, "error", err)
			}
		}()
	}

	if err := copyFile(binaryPath, destPath); err != nil {
		if _, e := os.Stat(backupPath); e == nil {
			slog.Warn("Restoring previous binary from backup")
			if restoreErr := os.Rename(backupPath, destPath); restoreErr != nil {
				slog.Error("CRITICAL: failed to restore backup", "error", restoreErr)
			}
		}
		os.RemoveAll(srcDir)
		events <- ProgressEvent{Stage: "install", Status: types.ProgressFailed, Message: err.Error()}
		return err
	}
	os.Chmod(destPath, 0755)
	os.RemoveAll(srcDir)
	events <- ProgressEvent{Stage: "install", Status: types.ProgressDone}

	// ----------------
	// 5. Restart
	// ----------------
	events <- ProgressEvent{Stage: "restart", Status: types.ProgressInProgress, Message: "Triggering restart..."}
	if err := mgr.Restart(); err != nil {
		slog.Warn("Post-update restart failed", "error", err)
		events <- ProgressEvent{Stage: "restart", Status: types.ProgressFailed, Message: err.Error()}
	} else {
		events <- ProgressEvent{Stage: "restart", Status: types.ProgressDone}
	}

	return nil
}

func errUnknownSlug(slug runtime.Slug) error {
	return fmt.Errorf("unknown service slug: %s", slug)
}

// DetectArchitecture returns the system architecture.
func DetectArchitecture() string {
	arch := goRuntime.GOARCH
	switch arch {
	case "amd64", "arm64":
		return arch
	default:
		return arch
	}
}
