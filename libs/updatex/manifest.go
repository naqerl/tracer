package updatex

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Manifest records the result of a successful update activation. It is written
// to the binary directory so that the process can report its version after a
// restart.
type Manifest struct {
	BinaryPath   string    `json:"binary_path"`
	Version      string    `json:"version"`
	Checksum     string    `json:"checksum,omitempty"`
	DownloadedAt time.Time `json:"downloaded_at,omitempty"`
	ActivatedAt  time.Time `json:"activated_at"`
}

// ManifestPath returns the path of the manifest file for the given binary directory.
func ManifestPath(binaryDir string) string {
	return filepath.Join(binaryDir, ".update-manifest.json")
}

// ReadManifest reads the manifest from the given binary directory.
// Returns nil if no manifest exists.
func ReadManifest(binaryDir string) (*Manifest, error) {
	path := ManifestPath(binaryDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("could not read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("could not decode manifest %s: %w", path, err)
	}
	return &m, nil
}

// WriteManifest writes the manifest to the given binary directory.
func WriteManifest(binaryDir string, m *Manifest) error {
	path := ManifestPath(binaryDir)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("could not encode manifest: %w", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("could not write manifest %s: %w", path, err)
	}
	return nil
}
