// Package kubeconfig installs a kubeconfig that the collector service user
// can read, so kubectl log sources keep working when the daemon runs without
// a home directory or with restricted permissions. The kubeconfig lives at
// /etc/tracer/kubeconfig (root-owned, readable by the tracer group).
package kubeconfig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"tracer/build"
	"tracer/libs/installx"
)

// Path is where the collector's kubeconfig lives.
const Path = "/etc/tracer/kubeconfig"

// ErrRequiresRoot is returned by Install when the process lacks privileges to
// write the collector's kubeconfig. Callers can use errors.Is to give the user
// a precise "re-run with sudo" message.
var ErrRequiresRoot = errors.New("requires root")

// Current returns the path to an existing kubeconfig for the current user.
// Candidates are tried in order: $KUBECONFIG (colon-separated, like kubectl
// itself), ~/.kube/config, then well-known system locations (k3s, kubeadm).
func Current() (string, error) {
	var candidates []string
	if env := strings.TrimSpace(os.Getenv("KUBECONFIG")); env != "" {
		for _, p := range filepath.SplitList(env) {
			if strings.TrimSpace(p) != "" {
				candidates = append(candidates, p)
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".kube", "config"))
	}
	candidates = append(candidates,
		"/etc/rancher/k3s/k3s.yaml",
		"/etc/kubernetes/admin.conf",
	)

	tried := make([]string, 0, len(candidates))
	for _, c := range candidates {
		info, err := os.Stat(c)
		if err != nil {
			tried = append(tried, c)
			continue
		}
		if info.IsDir() {
			tried = append(tried, c)
			continue
		}
		return c, nil
	}
	return "", fmt.Errorf("could not find a kubeconfig (tried %s)", strings.Join(tried, ", "))
}

// Verify checks that kubectl can load the kubeconfig and reach the cluster.
// It uses `kubectl get --raw /version`, which authenticates and talks to the
// API server without needing any namespace-scoped RBAC, so a kubeconfig
// scoped to pod/deployment log access still verifies. The failure message
// includes the command's stderr so the actual problem (unreadable file, bad
// server address, expired certificate) is visible.
func Verify(kubeconfig string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", "get", "--raw", "/version", "--kubeconfig", kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl could not use %s: %w: %s", kubeconfig, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Resolves verifies that kubectl can read the given resource in the namespace
// using the kubeconfig. It mirrors the add command's source detection, but
// against a specific kubeconfig, so add can prove the installed config works
// for the source being added.
func Resolves(kubeconfig, resource, namespace string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", "get", resource, "-n", namespace, "--no-headers", "-o", "name", "--kubeconfig", kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("kubectl could not read %s in namespace %s with %s: %w: %s",
			resource, namespace, kubeconfig, err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("kubectl found nothing for %s in namespace %s with %s", resource, namespace, kubeconfig)
	}
	return nil
}

// Install copies the given kubeconfig to Path and hands read access to the
// tracer group so the collector service user can use it. The daemon never
// writes the kubeconfig, so it stays root-owned. Must run as root.
func Install(src string) error {
	if os.Getuid() != 0 {
		return fmt.Errorf("installing %s: %w (run with sudo)", Path, ErrRequiresRoot)
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("could not read %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(Path), 0755); err != nil {
		return fmt.Errorf("could not create %s: %w", filepath.Dir(Path), err)
	}
	if err := os.WriteFile(Path, data, 0640); err != nil {
		return fmt.Errorf("could not write %s: %w", Path, err)
	}
	// os.WriteFile applies the process umask; pin the mode explicitly so the
	// tracer group actually gets read access regardless of the caller's umask.
	if err := os.Chmod(Path, 0640); err != nil {
		return fmt.Errorf("could not chmod %s: %w", Path, err)
	}
	if err := installx.ChownFile(Path, "root", build.TracerGroup); err != nil {
		return fmt.Errorf("could not chown %s: %w", Path, err)
	}
	return nil
}

// Ensure returns the collector's kubeconfig path, installing one when needed.
//
//   - preferred != "": install that kubeconfig (overwriting any existing
//     /etc/tracer/kubeconfig), so an explicit --kubeconfig always wins and a
//     stale installed file cannot dead-end the caller.
//   - preferred == "" and Path exists: reuse the installed file untouched.
//   - preferred == "": auto-detect and install.
func Ensure(preferred string) (string, error) {
	if preferred == "" && installx.FileExists(Path) {
		return Path, nil
	}
	src := preferred
	if src == "" {
		var err error
		src, err = Current()
		if err != nil {
			return "", err
		}
	}
	if err := Install(src); err != nil {
		return "", err
	}
	return Path, nil
}
