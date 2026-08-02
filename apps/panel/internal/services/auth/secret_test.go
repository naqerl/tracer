package auth

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tracer/apps/panel/internal/testutil"
)

func TestEncryptDecryptKeyRoundtrip(t *testing.T) {
	key := testutil.TestKeySecret()
	plaintext := "deadbeefcafebabe"

	encoded, err := encryptKey(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	if encoded == plaintext {
		t.Fatal("expected ciphertext to differ from plaintext")
	}

	decrypted, err := decryptKey(key, encoded)
	if err != nil {
		t.Fatalf("decrypt key: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("expected decrypted key %q, got %q", plaintext, decrypted)
	}
}

func TestEncryptKeyIsRandomized(t *testing.T) {
	key := testutil.TestKeySecret()
	plaintext := "same-plaintext-every-time"

	first, err := encryptKey(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	second, err := encryptKey(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	if first == second {
		t.Fatal("expected two encryptions of the same plaintext to differ (random nonce)")
	}
}

func TestDecryptKeyEmptyCiphertextReturnsErrKeyNotRevealable(t *testing.T) {
	_, err := decryptKey(testutil.TestKeySecret(), "")
	if !errors.Is(err, ErrKeyNotRevealable) {
		t.Fatalf("expected ErrKeyNotRevealable, got %v", err)
	}
}

func TestDecryptKeyTamperedCiphertextReturnsErrKeyDecryptFailed(t *testing.T) {
	key := testutil.TestKeySecret()

	encoded, err := encryptKey(key, "secret-key")
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}

	// Flip the last character so the GCM authentication tag fails.
	tampered := encoded[:len(encoded)-1]
	if encoded[len(encoded)-1] == 'A' {
		tampered += "B"
	} else {
		tampered += "A"
	}

	_, err = decryptKey(key, tampered)
	if !errors.Is(err, ErrKeyDecryptFailed) {
		t.Fatalf("expected ErrKeyDecryptFailed for tampered ciphertext, got %v", err)
	}
}

func TestDecryptKeyWrongMasterKeyReturnsErrKeyDecryptFailed(t *testing.T) {
	encoded, err := encryptKey(testutil.TestKeySecret(), "secret-key")
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}

	other := []byte("fedcba9876543210fedcba9876543210")
	if string(other) == string(testutil.TestKeySecret()) {
		t.Fatal("test setup: expected different master keys")
	}

	_, err = decryptKey(other, encoded)
	if !errors.Is(err, ErrKeyDecryptFailed) {
		t.Fatalf("expected ErrKeyDecryptFailed for wrong master key, got %v", err)
	}
}

func TestLoadKeySecretCreatesFileWith0600(t *testing.T) {
	dir := t.TempDir()
	key, err := LoadKeySecret(dir)
	if err != nil {
		t.Fatalf("load key secret: %v", err)
	}
	if len(key) != keySecretSize {
		t.Fatalf("expected %d-byte key, got %d", keySecretSize, len(key))
	}

	// A second load must return the same persisted key.
	again, err := LoadKeySecret(dir)
	if err != nil {
		t.Fatalf("load key secret again: %v", err)
	}
	if string(key) != string(again) {
		t.Fatal("expected the same key secret on reload")
	}

	info, err := os.Stat(filepath.Join(dir, keySecretFile))
	if err != nil {
		t.Fatalf("stat key secret file: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected key secret file mode 0600, got %v", info.Mode())
	}
}

func TestLoadKeySecretRejectsInvalidEnvSecret(t *testing.T) {
	t.Setenv("TRACER_KEY_SECRET", "not-hex")
	if _, err := LoadKeySecret(t.TempDir()); err == nil {
		t.Fatal("expected error for non-hex TRACER_KEY_SECRET")
	}

	t.Setenv("TRACER_KEY_SECRET", strings.Repeat("ab", 32))
	if _, err := LoadKeySecret(t.TempDir()); err != nil {
		t.Fatalf("expected valid 32-byte hex secret to load, got %v", err)
	}
}

func TestLoadKeySecretPrefersEnvSecret(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TRACER_KEY_SECRET", strings.Repeat("cd", 32))

	key, err := LoadKeySecret(dir)
	if err != nil {
		t.Fatalf("load key secret: %v", err)
	}
	if got := hex.EncodeToString(key); got != strings.Repeat("cd", 32) {
		t.Fatalf("expected env secret to win, got %q", got)
	}
}
