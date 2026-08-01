package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// keySecretFile is the name of the file in the data dir that holds the master
// key used to encrypt API key plaintext at rest.
const keySecretFile = "keys.key"

// keySecretSize is the size of the AES-256 master key.
const keySecretSize = 32

var (
	// ErrKeyNotRevealable is returned when a key has no stored ciphertext,
	// i.e. it was created before reveal support existed.
	ErrKeyNotRevealable = errors.New("api key was created before reveal support and cannot be shown again")
	// ErrKeyDecryptFailed is returned when the stored ciphertext cannot be
	// decrypted (tampering, wrong or missing key secret).
	ErrKeyDecryptFailed = errors.New("could not decrypt api key")
)

// LoadKeySecret loads the master key used to encrypt API key plaintext at rest.
// The secret comes from the TRACER_KEY_SECRET env var (hex, 32 bytes) when set,
// otherwise from a {dataDir}/keys.key file that is auto-created with 0600
// permissions on first use.
func LoadKeySecret(dataDir string) ([]byte, error) {
	if env := os.Getenv("TRACER_KEY_SECRET"); env != "" {
		key, err := hex.DecodeString(strings.TrimSpace(env))
		if err != nil {
			return nil, fmt.Errorf("could not decode TRACER_KEY_SECRET: %w", err)
		}
		if len(key) != keySecretSize {
			return nil, fmt.Errorf("TRACER_KEY_SECRET must be %d bytes (%d hex chars), got %d", keySecretSize, keySecretSize*2, len(key))
		}
		return key, nil
	}

	path := filepath.Join(dataDir, keySecretFile)
	raw, err := os.ReadFile(path)
	if err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil {
			return nil, fmt.Errorf("could not decode key secret file: %w", err)
		}
		if len(key) != keySecretSize {
			return nil, fmt.Errorf("key secret file must contain %d bytes (%d hex chars), got %d", keySecretSize, keySecretSize*2, len(key))
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("could not read key secret file: %w", err)
	}

	// First use: generate and persist a fresh secret.
	key := make([]byte, keySecretSize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("could not generate key secret: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("could not create data dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0600); err != nil {
		return nil, fmt.Errorf("could not write key secret file: %w", err)
	}
	return key, nil
}

// encryptKey encrypts plaintext with the master key using AES-256-GCM and
// returns base64(nonce || ciphertext).
func encryptKey(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("could not create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("could not create gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("could not generate nonce: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

// decryptKey reverses encryptKey. An empty encoded value means the key was
// created before reveal support and returns ErrKeyNotRevealable.
func decryptKey(key []byte, encoded string) (string, error) {
	if encoded == "" {
		return "", ErrKeyNotRevealable
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("could not decode api key ciphertext: %w", errors.Join(ErrKeyDecryptFailed, err))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("could not create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("could not create gcm: %w", err)
	}
	if len(raw) < gcm.NonceSize() {
		return "", ErrKeyDecryptFailed
	}
	nonce, sealed := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("could not open api key ciphertext: %w", errors.Join(ErrKeyDecryptFailed, err))
	}
	return string(plaintext), nil
}
