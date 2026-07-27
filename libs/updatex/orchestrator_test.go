package updatex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── Helpers ──────────────────────────────────────────────────────────────────

type orchTestEnv struct {
	binaryPath string
	binaryDir  string
	version    string
}

func (e *orchTestEnv) BinaryPath() string                                    { return e.binaryPath }
func (e *orchTestEnv) BinaryDir() string                                     { return e.binaryDir }
func (e *orchTestEnv) VersionOf(_ context.Context, _ string) (string, error) { return e.version, nil }

// withStagedBinary creates a temporary test environment with a dummy staged binary.
func withStagedBinary(t *testing.T) (*orchTestEnv, *Orchestrator, string) {
	t.Helper()
	dir := t.TempDir()
	binPath := filepath.Join(dir, "tracer-binary")
	env := &orchTestEnv{binaryDir: dir, binaryPath: binPath, version: "v1.0.0"}

	// Create a "current binary" so BinaryPath exists.
	if err := os.WriteFile(binPath, []byte("current binary content"), 0755); err != nil {
		t.Fatal(err)
	}

	orch := NewOrchestrator(env)
	staged := orch.StagedPath()

	// Create a staged binary.
	if err := os.WriteFile(staged, []byte("staged binary content"), 0755); err != nil {
		t.Fatal(err)
	}

	return env, orch, staged
}

// ── StagedPath ───────────────────────────────────────────────────────────────

func TestStagedPath(t *testing.T) {
	dir := t.TempDir()
	env := &orchTestEnv{binaryDir: dir}
	orch := NewOrchestrator(env)

	got := orch.StagedPath()
	want := filepath.Join(dir, stagedName)
	if got != want {
		t.Errorf("StagedPath = %q, want %q", got, want)
	}
}

// ── Verify ───────────────────────────────────────────────────────────────────

func TestVerify_Match(t *testing.T) {
	_, orch, _ := withStagedBinary(t)

	// Compute expected checksum.
	data, err := os.ReadFile(orch.StagedPath())
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	expected := hex.EncodeToString(h[:])

	if err := orch.Verify(expected); err != nil {
		t.Errorf("Verify matched checksum: %v", err)
	}
}

func TestVerify_Mismatch(t *testing.T) {
	_, orch, _ := withStagedBinary(t)

	err := orch.Verify("0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Error("expected error for mismatched checksum")
	}
}

func TestVerify_EmptyChecksum(t *testing.T) {
	_, orch, _ := withStagedBinary(t)

	if err := orch.Verify(""); err != nil {
		t.Errorf("Verify empty (skip) should not error: %v", err)
	}
}

func TestVerify_NoStagedBinary(t *testing.T) {
	dir := t.TempDir()
	env := &orchTestEnv{binaryDir: dir}
	orch := NewOrchestrator(env)

	err := orch.Verify("abc")
	if err == nil {
		t.Error("expected error when no staged binary exists")
	}
}

// ── Download ─────────────────────────────────────────────────────────────────

func TestDownload_Success(t *testing.T) {
	dir := t.TempDir()
	env := &orchTestEnv{binaryDir: dir}
	orch := NewOrchestrator(env)

	// Start a test server that serves a binary.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("new binary content"))
	}))
	defer ts.Close()

	ctx := context.Background()
	if err := orch.Download(ctx, ts.URL); err != nil {
		t.Fatalf("Download: %v", err)
	}

	// Verify the staged binary exists and has the right content.
	data, err := os.ReadFile(orch.StagedPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new binary content" {
		t.Errorf("staged content = %q, want %q", string(data), "new binary content")
	}

	// Verify it's executable.
	info, err := os.Stat(orch.StagedPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("staged binary is not executable")
	}
}

func TestDownload_HTTPError(t *testing.T) {
	dir := t.TempDir()
	env := &orchTestEnv{binaryDir: dir}
	orch := NewOrchestrator(env)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	ctx := context.Background()
	if err := orch.Download(ctx, ts.URL); err == nil {
		t.Error("expected error for HTTP 404")
	}
}

func TestDownload_NetworkError(t *testing.T) {
	dir := t.TempDir()
	env := &orchTestEnv{binaryDir: dir}
	orch := NewOrchestrator(env)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Connecting to a non-routable address should fail quickly.
	if err := orch.Download(ctx, "http://10.255.255.255:1/test"); err == nil {
		t.Error("expected error for unreachable URL")
	}
}

func TestDownload_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	env := &orchTestEnv{binaryDir: dir}
	orch := NewOrchestrator(env)

	// Create a previous staged binary.
	oldStaged := orch.StagedPath()
	if err := os.WriteFile(oldStaged, []byte("old content"), 0755); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("new content"))
	}))
	defer ts.Close()

	ctx := context.Background()
	if err := orch.Download(ctx, ts.URL); err != nil {
		t.Fatalf("Download: %v", err)
	}

	data, err := os.ReadFile(oldStaged)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new content" {
		t.Errorf("staged content = %q, want %q", string(data), "new content")
	}
}

// ── PreFlight ────────────────────────────────────────────────────────────────

func TestPreFlight_Success(t *testing.T) {
	env := &orchTestEnv{binaryDir: t.TempDir(), binaryPath: filepath.Join(t.TempDir(), "bin"), version: "v2.0.0"}
	orch := NewOrchestrator(env)
	staged := orch.StagedPath()
	if err := os.WriteFile(staged, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}

	version, err := orch.PreFlight(context.Background())
	if err != nil {
		t.Fatalf("PreFlight: %v", err)
	}
	if version != "v2.0.0" {
		t.Errorf("version = %q, want %q", version, "v2.0.0")
	}
}

func TestPreFlight_NoStagedBinary(t *testing.T) {
	dir := t.TempDir()
	env := &orchTestEnv{binaryDir: dir}
	orch := NewOrchestrator(env)

	if _, err := orch.PreFlight(context.Background()); err == nil {
		t.Error("expected error when no staged binary exists")
	}
}

// ── Activate ─────────────────────────────────────────────────────────────────

func TestActivate_Success(t *testing.T) {
	env, orch, staged := withStagedBinary(t)

	ctx := context.Background()
	manifest, err := orch.Activate(ctx)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}

	if manifest == nil {
		t.Fatal("expected non-nil manifest")
	}

	// The final binary should now have staged content.
	data, err := os.ReadFile(env.BinaryPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "staged binary content" {
		t.Errorf("binary content = %q, want %q", string(data), "staged binary content")
	}

	// The staged file should be gone.
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Error("staged file should be removed after activation")
	}

	// The backup should be removed.
	backup := orch.backupPath()
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Error("backup file should be removed after successful activation")
	}

	// Manifest should describe the activation.
	if manifest.BinaryPath != env.BinaryPath() {
		t.Errorf("BinaryPath = %q, want %q", manifest.BinaryPath, env.BinaryPath())
	}
	if manifest.Version != "v1.0.0" {
		t.Errorf("Version = %q, want %q", manifest.Version, "v1.0.0")
	}
	if manifest.ActivatedAt.IsZero() {
		t.Error("ActivatedAt should be set")
	}
}

func TestActivate_NoStagedBinary(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "tracer-binary")
	env := &orchTestEnv{binaryDir: dir, binaryPath: binPath, version: "v1.0.0"}
	if err := os.WriteFile(binPath, []byte("current"), 0755); err != nil {
		t.Fatal(err)
	}

	orch := NewOrchestrator(env)
	if _, err := orch.Activate(context.Background()); err == nil {
		t.Error("expected error when no staged binary exists")
	}
}

// failVersionEnv returns an error from VersionOf, simulating a broken binary.
type failVersionEnv struct {
	*orchTestEnv
}

func (e *failVersionEnv) VersionOf(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("simulated version check failure")
}

func TestActivate_RestoresBackupOnFailedPostFlight(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "tracer-binary")
	currentContent := []byte("current binary content")
	if err := os.WriteFile(binPath, currentContent, 0755); err != nil {
		t.Fatal(err)
	}

	base := &orchTestEnv{
		binaryDir:  dir,
		binaryPath: binPath,
		version:    "",
	}
	env := &failVersionEnv{base}
	orch := NewOrchestrator(env)

	staged := orch.StagedPath()
	if err := os.WriteFile(staged, []byte("bad staged binary"), 0755); err != nil {
		t.Fatal(err)
	}

	_, err := orch.Activate(context.Background())
	if err == nil {
		t.Fatal("expected post-flight error")
	}

	// The original binary should be restored.
	data, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(currentContent) {
		t.Errorf("binary restored content = %q, want %q", string(data), string(currentContent))
	}
}

func TestActivate_ExecutableBit(t *testing.T) {
	env, orch, _ := withStagedBinary(t)

	ctx := context.Background()
	if _, err := orch.Activate(ctx); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	info, err := os.Stat(env.BinaryPath())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("activated binary is not executable")
	}
}

// ── Integration: UpdateBinary → ActivateStaged ──────────────────────────────

func TestUpdateBinaryRoundtrip(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "tracer-binary")
	if err := os.WriteFile(binPath, []byte("old version"), 0755); err != nil {
		t.Fatal(err)
	}

	env := &orchTestEnv{binaryDir: dir, binaryPath: binPath, version: "v2.0.0"}

	// Start a server serving the "new" binary.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("new version binary"))
	}))
	defer ts.Close()

	ctx := context.Background()

	// Stage the update.
	stagedPath, err := UpdateBinary(ctx, env, ts.URL, "")
	if err != nil {
		t.Fatalf("UpdateBinary: %v", err)
	}
	if stagedPath == "" {
		t.Fatal("UpdateBinary returned empty path")
	}

	// At this point, staged binary exists but main binary is unchanged.
	currentData, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(currentData) != "old version" {
		t.Errorf("binary before activate = %q, want %q", string(currentData), "old version")
	}

	// Activate.
	if err := ActivateStaged(ctx, env); err != nil {
		t.Fatalf("ActivateStaged: %v", err)
	}

	// Main binary is now the staged one.
	currentData, err = os.ReadFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(currentData) != "new version binary" {
		t.Errorf("binary after activate = %q, want %q", string(currentData), "new version binary")
	}

	// Staged file should be gone.
	if _, err := os.Stat(stagedPath); !os.IsNotExist(err) {
		t.Error("staged file should be removed after activation")
	}

	// Manifest should exist.
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected manifest after activation")
	}
	if m.Version != "v2.0.0" {
		t.Errorf("manifest version = %q, want %q", m.Version, "v2.0.0")
	}
}
