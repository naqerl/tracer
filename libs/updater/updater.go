// Package updater provides self-update functionality for tracer binaries.
package updater

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

const repo = "ai-shift/tracer"

// BinaryType represents the type of tracer binary.
type BinaryType string

const (
	BinaryCollector BinaryType = "tracer-collector"
	BinaryPanel     BinaryType = "tracer-panel"
)

// Options configures the update process.
type Options struct {
	BinaryName BinaryType
	PostUpdate func()
}

// Update performs a self-update for the given binary type.
func Update(opts Options) error {
	// Get current executable path
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("get executable path: %w", err)
	}
	// Resolve symlinks to get the real path
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}

	// Detect architecture
	arch := runtime.GOARCH
	switch arch {
	case "amd64", "arm64":
		// supported
	default:
		return fmt.Errorf("unsupported architecture: %s", arch)
	}

	// Fetch latest version
	fmt.Println("Checking for latest version...")
	version, err := fetchLatestVersion()
	if err != nil {
		return fmt.Errorf("fetch latest version: %w", err)
	}
	fmt.Printf("Latest version: %s\n", version)

	// Download URL
	binaryName := string(opts.BinaryName)
	downloadURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/tracer-%s-linux-%s.tar.gz", repo, version, version, arch)
	fmt.Printf("Downloading from: %s\n", downloadURL)

	// Download tarball
	tmpFile, err := downloadToTemp(downloadURL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer os.Remove(tmpFile)

	// Extract tarball
	fmt.Println("Extracting...")
	if err := extractBinary(tmpFile, exePath, binaryName); err != nil {
		return fmt.Errorf("extract: %w", err)
	}

	// Show success message and post-update instructions
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("Update successful!")
	fmt.Println("========================================")
	fmt.Println()

	if opts.PostUpdate != nil {
		opts.PostUpdate()
	}

	return nil
}

// fetchLatestVersion retrieves the latest release tag from GitHub.
func fetchLatestVersion() (string, error) {
	latestURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	resp, err := http.Get(latestURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	return release.TagName, nil
}

// downloadToTemp downloads a URL to a temporary file and returns the path.
func downloadToTemp(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "tracer-update-*.tar.gz")
	if err != nil {
		return "", err
	}
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		os.Remove(tmpFile.Name())
		return "", err
	}

	return tmpFile.Name(), nil
}

// extractBinary extracts a specific binary from a tarball to the destination path.
func extractBinary(tarballPath, destPath, binaryName string) error {
	f, err := os.Open(tarballPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if header.Name != binaryName {
			continue
		}

		// Create temp file in same directory as destination
		tmpFile, err := os.CreateTemp(filepath.Dir(destPath), "."+binaryName+"-new-*")
		if err != nil {
			return err
		}
		tmpPath := tmpFile.Name()

		// Copy binary
		if _, err := io.Copy(tmpFile, tr); err != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			return err
		}
		tmpFile.Close()

		// Make executable
		if err := os.Chmod(tmpPath, 0755); err != nil {
			os.Remove(tmpPath)
			return err
		}

		// Rename to replace original
		if err := os.Rename(tmpPath, destPath); err != nil {
			// Try copy if rename fails (e.g., cross-device)
			if err := copyFile(tmpPath, destPath); err != nil {
				os.Remove(tmpPath)
				return err
			}
			os.Remove(tmpPath)
		}

		return nil
	}

	return fmt.Errorf("%s binary not found in tarball", binaryName)
}

// copyFile copies a file from src to dst with executable permissions.
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	if _, err := io.Copy(destFile, sourceFile); err != nil {
		return err
	}

	return destFile.Chmod(0755)
}
