package update

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"

	"tracer/libs/shared/runtime"
)

// Command represents a single, executable step of the update process.
type Command interface {
	Name() string
	Execute(ctx context.Context) error
}

// progressProxy wraps any Command to emit progress events before and after execution.
type progressProxy struct {
	cmd    Command
	events chan<- ProgressEvent
}

func (p *progressProxy) Name() string {
	return p.cmd.Name()
}

func (p *progressProxy) Execute(ctx context.Context) error {
	stage := p.cmd.Name()
	p.events <- ProgressEvent{Stage: stage, Status: StatusInProgress}
	if err := p.cmd.Execute(ctx); err != nil {
		p.events <- ProgressEvent{Stage: stage, Status: StatusFailed, Message: err.Error()}
		return err
	}
	p.events <- ProgressEvent{Stage: stage, Status: StatusDone}
	return nil
}

// downloadCommand executes the download stage of the update.
type downloadCommand struct {
	srcDir string
}

func (c *downloadCommand) Name() string { return StageDownload }
func (c *downloadCommand) Execute(ctx context.Context) error {
	if err := os.MkdirAll(c.srcDir, 0755); err != nil {
		return err
	}
	return downloadSnapshot(c.srcDir)
}

// buildCommand executes the build stage of the update.
type buildCommand struct {
	srcDir string
	slug   runtime.Slug
}

func (c *buildCommand) Name() string { return StageBuild }
func (c *buildCommand) Execute(ctx context.Context) error {
	var buildTarget string
	switch c.slug {
	case runtime.SlugCollector:
		buildTarget = "build-collector"
	case runtime.SlugPanel:
		buildTarget = "build-panel"
	default:
		return errUnknownSlug(c.slug)
	}
	cmd := exec.CommandContext(ctx, "make", buildTarget)
	cmd.Dir = c.srcDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// verifyCommand executes the verification stage of the update.
type verifyCommand struct {
	binaryPath string
}

func (c *verifyCommand) Name() string { return StageVerify }
func (c *verifyCommand) Execute(ctx context.Context) error {
	return VerifyBinary(c.binaryPath)
}

// installCommand executes the installation stage of the update.
type installCommand struct {
	binaryPath string
	destPath   string
}

func (c *installCommand) Name() string { return StageInstall }
func (c *installCommand) Execute(ctx context.Context) error {
	// Backup existing binary if present
	if _, err := os.Stat(c.destPath); err == nil {
		backupPath := c.destPath + ".bak"
		if err := copyFile(c.destPath, backupPath); err != nil {
			return err
		}
		defer func() {
			if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
				slog.Warn("Could not remove backup", "path", backupPath, "error", err)
			}
		}()
	}

	if err := copyFile(c.binaryPath, c.destPath); err != nil {
		return err
	}
	return os.Chmod(c.destPath, 0755)
}

// restartCommand executes the restart stage of the update.
type restartCommand struct {
	mgr runtime.Manager
}

func (c *restartCommand) Name() string { return StageRestart }
func (c *restartCommand) Execute(ctx context.Context) error {
	return c.mgr.Restart()
}

// Updater orchestrates the stages of a self-update (download, build, verify,
// install, restart) and reports progress via a Report.
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
func (u *Updater) run(events chan<- ProgressEvent) error {
	srcDir := "/tmp/tracer-update"
	os.RemoveAll(srcDir)
	defer os.RemoveAll(srcDir)

	ctx := context.Background()

	fmt.Println("Updating tracer from source...")
	fmt.Printf("Repository: %s\n", repoURL)

	mgr := runtime.NewManager(u.slug)

	// List of mandatory stages
	commands := []Command{
		&downloadCommand{srcDir: srcDir},
		&buildCommand{srcDir: srcDir, slug: u.slug},
		&verifyCommand{binaryPath: binaryPath(srcDir, u.slug)},
		&installCommand{binaryPath: binaryPath(srcDir, u.slug), destPath: destPath(mgr, u.slug)},
	}

	for _, cmd := range commands {
		proxy := &progressProxy{cmd: cmd, events: events}
		if err := proxy.Execute(ctx); err != nil {
			return err
		}
	}

	// Restart stage is optional/warning-only
	restartCmd := &restartCommand{mgr: mgr}
	proxy := &progressProxy{cmd: restartCmd, events: events}
	if err := proxy.Execute(ctx); err != nil {
		slog.Warn("Post-update restart failed", "error", err)
	} else {
		events <- ProgressEvent{Stage: StageComplete, Status: StatusDone}
	}

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

func errUnknownSlug(slug runtime.Slug) error {
	return fmt.Errorf("unknown service slug: %s", slug)
}
