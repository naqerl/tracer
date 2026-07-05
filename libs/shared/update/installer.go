package update

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"tracer/libs/shared/runtime"
)

// Installer performs the stages of a self-update. Each stage is a separate
// method so the caller can wrap or proxy them individually.
type Installer interface {
	Download(ctx context.Context, srcDir string) error
	Build(ctx context.Context, srcDir string, slug runtime.Slug) error
	Verify(ctx context.Context, binaryPath string) error
	Install(ctx context.Context, binaryPath, destPath string) error
	Restart(ctx context.Context, mgr runtime.Manager) error
}

// realInstaller is the production Installer that talks to the real system.
type realInstaller struct{}

func (realInstaller) Download(ctx context.Context, srcDir string) error {
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return err
	}
	return downloadSnapshot(srcDir)
}

func (realInstaller) Build(ctx context.Context, srcDir string, slug runtime.Slug) error {
	var buildTarget string
	switch slug {
	case runtime.SlugCollector:
		buildTarget = "build-collector"
	case runtime.SlugPanel:
		buildTarget = "build-panel"
	default:
		return errUnknownSlug(slug)
	}
	cmd := exec.Command("make", buildTarget)
	cmd.Dir = srcDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (realInstaller) Verify(ctx context.Context, binaryPath string) error {
	return VerifyBinary(binaryPath)
}

// Install copies the built binary to the destination, with a backup of the
// existing binary if present.
func (realInstaller) Install(ctx context.Context, binaryPath, destPath string) error {
	// Backup existing binary if present
	if _, err := os.Stat(destPath); err == nil {
		backupPath := destPath + ".bak"
		if err := copyFile(destPath, backupPath); err != nil {
			return err
		}
		defer func() {
			if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
				slog.Warn("Could not remove backup", "path", backupPath, "error", err)
			}
		}()
	}

	if err := copyFile(binaryPath, destPath); err != nil {
		return err
	}
	os.Chmod(destPath, 0755)
	return nil
}

func (realInstaller) Restart(ctx context.Context, mgr runtime.Manager) error {
	return mgr.Restart()
}

// ProgressProxy wraps an Installer and emits a ProgressEvent to its Events
// channel before and after each method call.  Each stage method produces at
// most two events: in_progress then done or failed.
type ProgressProxy struct {
	inner  Installer
	Events chan<- ProgressEvent
}

// NewProgressProxy creates a proxy that wraps inner and emits progress
// events to the given channel for every stage method.
func NewProgressProxy(inner Installer, events chan<- ProgressEvent) *ProgressProxy {
	return &ProgressProxy{
		inner:  inner,
		Events: events,
	}
}

func (p *ProgressProxy) Download(ctx context.Context, srcDir string) error {
	p.Events <- ProgressEvent{Stage: StageDownload, Status: StatusInProgress}
	if err := p.inner.Download(ctx, srcDir); err != nil {
		p.Events <- ProgressEvent{Stage: StageDownload, Status: StatusFailed, Message: err.Error()}
		return err
	}
	p.Events <- ProgressEvent{Stage: StageDownload, Status: StatusDone}
	return nil
}

func (p *ProgressProxy) Build(ctx context.Context, srcDir string, slug runtime.Slug) error {
	p.Events <- ProgressEvent{Stage: StageBuild, Status: StatusInProgress}
	if err := p.inner.Build(ctx, srcDir, slug); err != nil {
		p.Events <- ProgressEvent{Stage: StageBuild, Status: StatusFailed, Message: err.Error()}
		return err
	}
	p.Events <- ProgressEvent{Stage: StageBuild, Status: StatusDone}
	return nil
}

func (p *ProgressProxy) Verify(ctx context.Context, binaryPath string) error {
	p.Events <- ProgressEvent{Stage: StageVerify, Status: StatusInProgress}
	if err := p.inner.Verify(ctx, binaryPath); err != nil {
		p.Events <- ProgressEvent{Stage: StageVerify, Status: StatusFailed, Message: err.Error()}
		return err
	}
	p.Events <- ProgressEvent{Stage: StageVerify, Status: StatusDone}
	return nil
}

func (p *ProgressProxy) Install(ctx context.Context, binaryPath, destPath string) error {
	p.Events <- ProgressEvent{Stage: StageInstall, Status: StatusInProgress}
	if err := p.inner.Install(ctx, binaryPath, destPath); err != nil {
		p.Events <- ProgressEvent{Stage: StageInstall, Status: StatusFailed, Message: err.Error()}
		return err
	}
	p.Events <- ProgressEvent{Stage: StageInstall, Status: StatusDone}
	return nil
}

func (p *ProgressProxy) Restart(ctx context.Context, mgr runtime.Manager) error {
	p.Events <- ProgressEvent{Stage: StageRestart, Status: StatusInProgress}
	if err := p.inner.Restart(ctx, mgr); err != nil {
		p.Events <- ProgressEvent{Stage: StageRestart, Status: StatusFailed, Message: err.Error()}
		return err
	}
	p.Events <- ProgressEvent{Stage: StageRestart, Status: StatusDone}
	return nil
}

// binaryPath returns the path to the built binary for the given slug.
func binaryPath(srcDir string, slug runtime.Slug) string {
	return filepath.Join(srcDir, "bin", string(slug))
}

// destPath returns the canonical install destination for the given slug.
func destPath(mgr runtime.Manager, slug runtime.Slug) string {
	return filepath.Join(mgr.BinaryDir(), string(slug))
}
