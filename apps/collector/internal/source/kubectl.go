package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
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

	// resolved records that a bare name has already been looked up among the
	// workload kinds, so a retry that finds nothing does not ask the API
	// server for every kind again.
	resolved bool

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

// workloadKinds are tried, in order, when a bare name matches no pod. kubectl
// resolves an unqualified name as a pod, so a workload name such as `barse`
// fails with NotFound unless its kind is spelled out.
var workloadKinds = []string{"deployment", "statefulset", "daemonset", "replicaset", "replicationcontroller", "job", "cronjob"}

// hasResourceKind reports whether the resource already names its kind, either
// as `deploy/barse` or via a selector.
func hasResourceKind(resource string) bool {
	if strings.HasPrefix(resource, "-l ") || strings.HasPrefix(resource, "--selector ") {
		return true
	}
	_, _, found := strings.Cut(resource, "/")
	return found
}

// resolveBareName rewrites a bare workload name to its kind-qualified form.
//
// A name with no `/` is passed to kubectl as a pod name, which is correct for
// pods but fails for a Deployment, StatefulSet, or Job even when that workload
// exists. When the bare name matches no pod, each workload kind is queried and
// the first match is used, so entering `barse` finds the deployment.
func (ks *kubectlSource) resolveBareName(ctx context.Context) {
	resource := strings.TrimSpace(ks.resource)
	if hasResourceKind(resource) {
		return
	}
	ks.resolved = true

	if ks.resourceExists(ctx, resource, "pod") {
		return
	}

	for _, kind := range workloadKinds {
		if !ks.resourceExists(ctx, resource, kind) {
			continue
		}
		slog.Debug("kubectl source: resolved bare name to workload kind",
			"resource", resource, "kind", kind, "namespace", ks.namespace)
		ks.resource = kind + "/" + resource
		return
	}
}

// resourceExists reports whether a named resource of the given kind is present.
func (ks *kubectlSource) resourceExists(ctx context.Context, name, kind string) bool {
	args := []string{"get", kind, name, "-n", ks.namespace, "-o", "name"}
	if ks.kubeconfig != "" {
		args = append(args, "--kubeconfig", ks.kubeconfig)
	}
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	out, err := cmd.Output()
	if err != nil {
		// A missing resource and a broken connection are indistinguishable
		// here on purpose: either way there is nothing to resolve to.
		return false
	}
	return strings.TrimSpace(string(out)) != ""
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
			stderr := strings.TrimSpace(ks.stderr.String())
			if isNoSuchResource(stderr) {
				// A bare name is resolved as a pod by kubectl, so a workload
				// like `barse` fails even though the deployment exists. Look for
				// the workload kind once, then retry with it.
				if !ks.resolved && !hasResourceKind(ks.resource) {
					original := ks.resource
					ks.resolveBareName(ctx)
					if ks.resource != original {
						if err := ks.Close(); err != nil {
							return "", fmt.Errorf("could not reset kubectl source: %w", err)
						}
						if err := ks.startKubectl(follow); err != nil {
							return "", err
						}
						return ks.Next(ctx)
					}
				}
				return "", NotFound(fmt.Errorf("kubectl logs (%s/%s): %s",
					ks.namespace, ks.resource, stderr))
			}
			return "", fmt.Errorf("kubectl logs (%s/%s) exited: %w: %s",
				ks.namespace, ks.resource, waitErr, stderr)
		}
		return "", io.EOF
	}
}

// isNoSuchResource reports whether kubectl failed because the resource does not
// exist. A kubeconfig the service user cannot read, or a forbidden resource,
// must not be reported as absent.
func isNoSuchResource(stderr string) bool {
	lower := strings.ToLower(stderr)
	if strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "error loading config file") ||
		strings.Contains(lower, "is forbidden") {
		return false
	}
	return strings.Contains(lower, "notfound") ||
		strings.Contains(lower, "not found") ||
		strings.Contains(lower, "doesn't have a resource type") ||
		strings.Contains(lower, "the server doesn't have a resource type")
}

func (ks *kubectlSource) Close() error {
	if ks.cmd == nil {
		ks.scanner = nil
		ks.seeked = false
		return nil
	}
	if err := ks.cmd.Process.Kill(); err != nil {
		if !errors.Is(err, os.ErrProcessDone) {
			slog.Warn("kubectl source: failed to kill process", "error", err, "resource", ks.resource)
		}
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

	// Set kubeconfig if specified. cmd.Env starts nil, so appending to it
	// would replace the entire environment (dropping PATH, HOME, etc.);
	// inherit the parent env and add ours on top.
	if ks.kubeconfig != "" {
		cmd.Env = append(os.Environ(), "KUBECONFIG="+ks.kubeconfig)
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
