package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
)

// KubectlConfig contains configuration for kubectl log source.
type KubectlConfig struct {
	Resource   string // e.g. "deployment/myapp", "pod/myapp-xxx", "-l app=myapp"
	Namespace  string // Kubernetes namespace
	Container  string // Optional, empty = first/only container
	Kubeconfig string // Optional, empty = default kubeconfig path
}

// kubectlSource reads logs from Kubernetes pods via the kubectl CLI.
// It mirrors journalctlSource: runs an external command, streams stdout,
// and supports seeking by killing and restarting with different --tail values.
type kubectlSource struct {
	resource   string
	namespace  string
	container  string
	kubeconfig string

	cmd     *exec.Cmd
	scanner *bufio.Scanner
	ctx     context.Context // stored from Open for use in restart
	seeked  bool

	// stderr captures the running kubectl's stderr so a failing process can
	// report why it failed (e.g. an unreadable kubeconfig) instead of leaving
	// the caller with a bare EOF. waited tracks whether Next already called
	// cmd.Wait() so Close does not wait a second time.
	stderr *bytes.Buffer
	waited bool

	// linesFromEnd specifies how many lines to show from the end.
	//  0  = start from now (tail mode, default)
	// -1  = show all (from beginning)
	//  N  = show last N lines
	linesFromEnd int64
}

// NewKubectl creates a new kubectl source.
func NewKubectl(cfg KubectlConfig) *kubectlSource {
	return &kubectlSource{
		resource:     cfg.Resource,
		namespace:    cfg.Namespace,
		container:    cfg.Container,
		kubeconfig:   cfg.Kubeconfig,
		linesFromEnd: 0, // Default: start from now
	}
}

func (ks *kubectlSource) Open(ctx context.Context) error {
	if strings.TrimSpace(ks.resource) == "" {
		return errors.New("kubectl source: empty resource")
	}
	if strings.TrimSpace(ks.namespace) == "" {
		ks.namespace = "default"
	}
	ks.ctx = ctx
	return nil
}

func (ks *kubectlSource) Next(ctx context.Context) (string, error) {
	if ks.ctx == nil {
		return "", errors.New("kubectl source not open")
	}
	follow := true
	if ks.seeked {
		follow = false
		if err := ks.Close(); err != nil {
			return "", err
		}
		ks.seeked = false
	}

	if ks.cmd == nil {
		if err := ks.startKubectl(follow); err != nil {
			return "", err
		}
	}

	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if ks.scanner.Scan() {
			line := strings.TrimSpace(ks.scanner.Text())
			if line == "" {
				continue
			}
			return line, nil
		}

		if err := ks.scanner.Err(); err != nil {
			return "", fmt.Errorf("kubectl scanner (%s/%s): %w", ks.namespace, ks.resource, err)
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		// A previous Next already drained the stream and reaped the process;
		// keep reporting the documented EOF instead of calling Wait twice.
		if ks.waited {
			return "", io.EOF
		}

		// The stream ended. Wait() must run only after stdout is fully
		// drained (it closes the pipe), so this is the first time we reap.
		// A clean exit (status 0) means kubectl finished on its own —
		// report io.EOF so the caller can retry. A non-zero exit means
		// kubectl failed; stderr holds the reason.
		ks.waited = true
		if waitErr := ks.cmd.Wait(); waitErr != nil {
			return "", fmt.Errorf("kubectl logs (%s/%s) exited: %w: %s",
				ks.namespace, ks.resource, waitErr, strings.TrimSpace(ks.stderr.String()))
		}
		return "", io.EOF
	}
}

func (ks *kubectlSource) Close() error {
	if ks.cmd == nil {
		ks.scanner = nil
		ks.seeked = false
		return nil
	}
	if err := ks.cmd.Process.Kill(); err != nil {
		slog.Warn("kubectl source: failed to kill process", "error", err, "resource", ks.resource)
	}
	// Reap the process unless Next already waited after draining stdout.
	if !ks.waited {
		if err := ks.cmd.Wait(); err != nil {
			slog.Warn("kubectl source: failed to reap process", "error", err, "resource", ks.resource)
		}
	}

	ks.cmd = nil
	ks.scanner = nil
	ks.stderr = nil
	ks.waited = false
	ks.seeked = false

	return nil
}

// Seek implements io.Seeker for kubectlSource.
// Since kubectl doesn't support runtime seeking, this method kills the
// current process and restarts it with appropriate --tail flags.
//
// Position semantics:
//   - Seek(0, io.SeekStart): Read from beginning (all history)
//   - Seek(N, io.SeekStart): Not supported (kubectl can't skip first N lines)
//   - Seek(0, io.SeekEnd): Read from now (tail mode)
//   - Seek(-N, io.SeekEnd): Read last N lines, then follow
//   - Seek(0, io.SeekCurrent): No-op, returns 0
//
// Returns 0 for all successful seeks (positions are opaque).
func (ks *kubectlSource) Seek(offset int64, whence int) (int64, error) {
	if ks.ctx == nil {
		return 0, errors.New("kubectl source not open")
	}

	switch whence {
	case io.SeekStart:
		if offset != 0 {
			return 0, fmt.Errorf("kubectl source: SeekStart only supports offset=0, got %d", offset)
		}
		ks.linesFromEnd = -1 // From beginning
	case io.SeekEnd:
		if offset > 0 {
			return 0, errors.New("kubectl source: SeekEnd only supports offset<=0")
		}
		if offset == 0 {
			ks.linesFromEnd = 0 // From now
		} else {
			ks.linesFromEnd = -offset // Last N lines
		}
	case io.SeekCurrent:
		if offset != 0 {
			return 0, errors.New("kubectl source: SeekCurrent only supports offset=0 (query position)")
		}
		return 0, nil
	default:
		return 0, fmt.Errorf("invalid whence: %d", whence)
	}

	ks.seeked = true
	return 0, nil
}

func (ks *kubectlSource) startKubectl(follow bool) error {
	flags := ks.buildFlags(follow)
	cmd := exec.CommandContext(ks.ctx, "kubectl", flags...)

	// Set kubeconfig if specified
	if ks.kubeconfig != "" {
		cmd.Env = append(cmd.Env, "KUBECONFIG="+ks.kubeconfig)
	}

	// Capture stderr instead of letting it go to /dev/null, so a failing
	// kubectl (e.g. an unreadable kubeconfig) reports the real reason.
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("kubectl stdout pipe (%s/%s): %w", ks.namespace, ks.resource, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("kubectl start (%s/%s): %w", ks.namespace, ks.resource, err)
	}

	scanner := bufio.NewScanner(stdout)
	const maxLineBytes = 1024 * 1024
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	ks.cmd = cmd
	ks.scanner = scanner
	ks.stderr = stderr
	ks.waited = false

	return nil
}

// buildFlags constructs kubectl logs flags based on current mode.
// If resource starts with "-l " or "--selector ", it's treated as a label
// selector and split into separate arguments for kubectl.
func (ks *kubectlSource) buildFlags(follow bool) []string {
	var flags []string

	// Handle label selectors vs resource names
	if strings.HasPrefix(ks.resource, "-l ") || strings.HasPrefix(ks.resource, "--selector ") {
		parts := strings.SplitN(ks.resource, " ", 2)
		flags = append(flags, "logs", parts[0], strings.TrimSpace(parts[1]))
	} else {
		flags = append(flags, "logs", ks.resource)
	}

	// Do NOT pass --prefix: kubectl would prepend "[pod/<pod>/<container>] " to
	// every line, which corrupts JSON/slog text parsing (the prefix merges into
	// the first key, e.g. "[pod/x/y] time=..."). Emit raw container log lines.
	flags = append(flags, "-n", ks.namespace)

	if ks.container != "" {
		flags = append(flags, "-c", ks.container)
	}

	if follow {
		flags = append(flags, "-f")
	}

	switch ks.linesFromEnd {
	case -1:
		// From beginning: no --tail flag (shows all), possibly -f
	case 0:
		// From now: -f --tail=0
		flags = append(flags, "--tail=0")
	default:
		// Last N lines: --tail=N, possibly -f
		flags = append(flags, "--tail="+strconv.FormatInt(ks.linesFromEnd, 10))
	}

	return flags
}
