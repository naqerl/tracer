package kubeconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCurrentPrefersKubeconfigEnv(t *testing.T) {
	dir := t.TempDir()
	envPath := filepath.Join(dir, "env-config")
	if err := os.WriteFile(envPath, []byte("apiVersion: v1"), 0600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	homeCfg := filepath.Join(home, ".kube", "config")
	if err := os.MkdirAll(filepath.Dir(homeCfg), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homeCfg, []byte("apiVersion: v1"), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KUBECONFIG", envPath)
	t.Setenv("HOME", home)

	got, err := Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got != envPath {
		t.Fatalf("Current = %q, want %q (env should win over home)", got, envPath)
	}
}

func TestCurrentFallsBackToHomeConfig(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	cfgPath := filepath.Join(home, ".kube", "config")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte("apiVersion: v1"), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KUBECONFIG", "")
	t.Setenv("HOME", home)

	got, err := Current()
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if got != cfgPath {
		t.Fatalf("Current = %q, want %q", got, cfgPath)
	}
}

func TestCurrentMissing(t *testing.T) {
	// The test only holds on machines without a system kubeconfig; a k3s or
	// control-plane node has one at a well-known path and Current finds it.
	for _, sys := range []string{"/etc/rancher/k3s/k3s.yaml", "/etc/kubernetes/admin.conf"} {
		info, err := os.Stat(sys)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		t.Skipf("system kubeconfig %s exists on this host", sys)
	}

	t.Setenv("KUBECONFIG", "")
	t.Setenv("HOME", t.TempDir())

	if _, err := Current(); err == nil {
		t.Fatal("expected error when no kubeconfig exists")
	}
}

func TestResolvesWithFakeKubectl(t *testing.T) {
	dir := t.TempDir()

	// Fake kubectl that answers get like the real one but ignores extra args
	// such as --kubeconfig.
	script := filepath.Join(dir, "kubectl")
	body := "#!/bin/bash\ncase \"${1:-}\" in\n  get) echo pod/myapp-xxxxx ;;\n  *) echo \"unknown command: $1\" >&2; exit 1 ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	kc := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kc, []byte("apiVersion: v1"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := Resolves(kc, "deployment/myapp", "default"); err != nil {
		t.Fatalf("Resolves: %v", err)
	}
}

func TestResolvesReportsFailure(t *testing.T) {
	dir := t.TempDir()

	script := filepath.Join(dir, "kubectl")
	body := "#!/bin/bash\necho 'error: the server could not find the requested resource' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	kc := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kc, []byte("apiVersion: v1"), 0600); err != nil {
		t.Fatal(err)
	}

	err := Resolves(kc, "deployment/missing", "default")
	if err == nil {
		t.Fatal("expected an error when kubectl fails")
	}
	if !strings.Contains(err.Error(), "could not find the requested resource") {
		t.Fatalf("error should include kubectl stderr, got: %v", err)
	}
}
