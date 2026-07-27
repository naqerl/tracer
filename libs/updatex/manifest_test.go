package updatex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManifestRoundtrip(t *testing.T) {
	dir := t.TempDir()

	m := &Manifest{
		BinaryPath:  "/usr/local/bin/tracer-collector",
		Version:     "v1.2.3",
		Checksum:    "abc123",
		ActivatedAt: time.Now().Truncate(time.Second),
	}

	if err := WriteManifest(dir, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	got, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if got == nil {
		t.Fatal("ReadManifest returned nil")
	}

	if got.BinaryPath != m.BinaryPath {
		t.Errorf("BinaryPath = %q, want %q", got.BinaryPath, m.BinaryPath)
	}
	if got.Version != m.Version {
		t.Errorf("Version = %q, want %q", got.Version, m.Version)
	}
	if got.Checksum != m.Checksum {
		t.Errorf("Checksum = %q, want %q", got.Checksum, m.Checksum)
	}
}

func TestReadManifestNonexistent(t *testing.T) {
	dir := t.TempDir()
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if m != nil {
		t.Error("expected nil manifest for nonexistent file")
	}
}

func TestReadManifestCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".update-manifest.json")
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadManifest(dir)
	if err == nil {
		t.Error("expected error for corrupt manifest")
	}
}

func TestManifestPath(t *testing.T) {
	path := ManifestPath("/some/dir")
	want := "/some/dir/.update-manifest.json"
	if path != want {
		t.Errorf("ManifestPath = %q, want %q", path, want)
	}
}
