package source

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHasResourceKind covers the decision to resolve a bare name: only a name
// without a kind (and without a selector) needs looking up.
func TestHasResourceKind(t *testing.T) {
	tests := []struct {
		resource string
		want     bool
	}{
		{resource: "barse", want: false},
		{resource: "deploy/barse", want: true},
		{resource: "deployment.apps/barse", want: true},
		{resource: "pod/barse-0", want: true},
		{resource: "-l app=barse", want: true},
		{resource: "--selector app=barse", want: true},
	}
	for _, tt := range tests {
		if got := hasResourceKind(tt.resource); got != tt.want {
			t.Errorf("hasResourceKind(%q) = %v, want %v", tt.resource, got, tt.want)
		}
	}
}

// TestKubectlSourceResolvesBareNameToWorkloadKind covers the reported bug:
// `barse` names a deployment, but kubectl reads an unqualified name as a pod,
// so the first `logs` fails with NotFound and the source has to retry with the
// kind it found.
func TestKubectlSourceResolvesBareNameToWorkloadKind(t *testing.T) {
	writeFakeScript(t, `
if [ "$1" = "logs" ] && [ "$2" = "deployment/barse" ]; then
	echo '{"level":"info","msg":"resolved"}'
	exit 0
fi
if [ "$1" = "logs" ]; then
	echo 'error: error from server (NotFound): pods "barse" not found in namespace "default"' >&2
	exit 1
fi
if [ "$1" = "get" ] && [ "$2" = "deployment" ]; then
	echo 'deployment.apps/barse'
	exit 0
fi
if [ "$1" = "get" ]; then
	echo "Error from server (NotFound): $2 \"barse\" not found" >&2
	exit 1
fi
exit 1
`)

	src := NewKubectl(KubectlConfig{Resource: "barse", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	line, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !strings.Contains(line, "resolved") {
		t.Fatalf("expected the line from deployment/barse, got %q", line)
	}
	if want := "deployment/barse"; src.resource != want {
		t.Fatalf("resource = %q, want %q", src.resource, want)
	}
	if src.cmd == nil || src.scanner == nil {
		t.Fatal("the retried kubectl process is not running")
	}
	if src.stderr != nil && src.stderr.Len() != 0 {
		t.Fatalf("stderr of the failed attempt leaked into the new process: %q", src.stderr)
	}
}

// TestKubectlSourceKeepsPodResourceWhenPodExists guards the lookup behind the
// rewrite: a NotFound that names the pod and its container (the container was
// removed) must not send the source off to look for workloads.
func TestKubectlSourceKeepsPodResourceWhenPodExists(t *testing.T) {
	writeFakeScript(t, `
if [ "$1" = "logs" ]; then
	echo 'error: error from server (NotFound): containers "gone" not found in pod "barse"' >&2
	exit 1
fi
if [ "$1" = "get" ] && [ "$2" = "pod" ]; then
	echo 'pod/barse'
	exit 0
fi
if [ "$1" = "get" ]; then
	echo "Error from server (NotFound): $2 \"barse\" not found" >&2
	exit 1
fi
exit 1
`)

	src := NewKubectl(KubectlConfig{Resource: "barse", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	_, err := src.Next(t.Context())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if src.resource != "barse" {
		t.Fatalf("resource = %q, want the bare name %q", src.resource, "barse")
	}
}

// TestKubectlSourceBareNameWithoutWorkloadStaysNotFound covers a name that
// matches no pod and no workload: the failure must still name what was typed.
func TestKubectlSourceBareNameWithoutWorkloadStaysNotFound(t *testing.T) {
	writeFakeScript(t, `
echo "Error from server (NotFound): $2 \"does-not-exist\" not found" >&2
exit 1
`)

	src := NewKubectl(KubectlConfig{Resource: "does-not-exist", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	_, err := src.Next(t.Context())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error must name the typed resource, got: %v", err)
	}
	if src.resource != "does-not-exist" {
		t.Fatalf("resource = %q, want the bare name %q", src.resource, "does-not-exist")
	}
}

// TestKubectlSourceLooksUpEachWorkloadKindOnce covers which kinds are asked
// for, in which order, and that a retry after a failed lookup does not ask
// the API server all over again.
func TestKubectlSourceLooksUpEachWorkloadKindOnce(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	writeFakeScript(t, fmt.Sprintf(`
echo "$@" >> %s
echo "Error from server (NotFound): $2 \"barse\" not found" >&2
exit 1
`, calls))

	src := NewKubectl(KubectlConfig{Resource: "barse", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	if _, err := src.Next(t.Context()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}

	want := []string{"pod", "deployment", "statefulset", "daemonset", "replicaset", "replicationcontroller", "job", "cronjob"}
	if got := lookedUpKinds(t, calls); !equalStrings(got, want) {
		t.Fatalf("looked up %v, want %v", got, want)
	}

	// A retry on the same source must not repeat the lookups.
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Next(t.Context()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry error = %v, want ErrNotFound", err)
	}
	if got := lookedUpKinds(t, calls); !equalStrings(got, want) {
		t.Fatalf("retry looked up %v, want no further kinds after %v", got, want)
	}
}

// lookedUpKinds returns the kinds passed to `kubectl get` in the order they
// were asked for.
func lookedUpKinds(t *testing.T, calls string) []string {
	t.Helper()
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read recorded calls: %v", err)
	}
	var kinds []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "get" {
			kinds = append(kinds, fields[1])
		}
	}
	return kinds
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestKubectlSourceKeepsBareNameWhenPodExistsButLogsFail covers the pod that
// resolves but whose logs fail for another reason — no container named, for
// instance. That is not a missing workload, so the name must not be rewritten
// into a deployment lookup.
func TestKubectlSourceKeepsBareNameWhenPodExistsButLogsFail(t *testing.T) {
	writeFakeScript(t, `
case "$*" in
  "logs barse"*)
    echo 'error: a container name must be specified for pod barse' >&2
    exit 1 ;;
  "get pod barse"*)
    echo 'pod/barse' ;;
  *)
    echo "should not be asked to resolve a workload kind" >&2
    exit 1 ;;
esac
`)

	src := NewKubectl(KubectlConfig{Resource: "barse", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	if _, err := src.Next(t.Context()); err == nil {
		t.Fatal("expected an error")
	}
	if src.resource != "barse" {
		t.Errorf("resource = %q, want it left as barse", src.resource)
	}
}

// TestKubectlSourceResolvesStatefulset proves resolution is not
// deployment-only: a statefulset is found the same way a deployment is.
func TestKubectlSourceResolvesStatefulset(t *testing.T) {
	writeFakeScript(t, `
case "$1 $2 $3" in
  "get pod db"*)         echo 'Error from server (NotFound)' >&2; exit 1 ;;
  "get deployment db"*)  echo 'Error from server (NotFound)' >&2; exit 1 ;;
  "get statefulset db"*) echo 'statefulset.apps/db' ;;
esac
case "$1 $2" in
  "logs db")           echo 'error: error from server (NotFound): pods "db" not found' >&2; exit 1 ;;
  "logs statefulset/db") echo 'time=2026-10-02T12:00:00Z level=INFO msg=db-ready' ;;
esac
exit 0
`)

	src := NewKubectl(KubectlConfig{Resource: "db", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	line, err := src.Next(t.Context())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if !strings.Contains(line, "db-ready") {
		t.Errorf("line = %q, want the statefulset's log line", line)
	}
	if src.resource != "statefulset/db" {
		t.Errorf("resource = %q, want statefulset/db", src.resource)
	}
}

// TestKubectlSourceSkipsQualifiedResource proves a name that already carries its
// kind is never sent through the workload lookup.
func TestKubectlSourceSkipsQualifiedResource(t *testing.T) {
	writeFakeScript(t, `
case "$*" in
  "logs deploy/barse"*) echo 'time=2026-10-02T12:00:00Z level=INFO msg=ok' ;;
  *)                    echo "should not resolve a qualified name" >&2; exit 1 ;;
esac
`)

	src := NewKubectl(KubectlConfig{Resource: "deploy/barse", Namespace: "default"})
	if err := src.Open(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })

	if _, err := src.Next(t.Context()); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if src.resource != "deploy/barse" {
		t.Errorf("resource = %q, want it unchanged", src.resource)
	}
}
