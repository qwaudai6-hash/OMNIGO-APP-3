package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// EncryptAESGCM encrypts plaintext with AES-256-GCM and returns
// nonce(12) || ciphertext || tag(16) as a single byte slice.
//
// The 32-byte key is derived from the supplied passphrase via SHA-256
// so operators can configure a human-readable env var (ADMIN_API_KEY_ENCRYPTION_KEY)
// without worrying about exact byte length.
//
// Use this for at-rest protection of admin-managed API credentials.
// The same passphrase MUST be configured on every service that needs
// to read these credentials (currently only admin-service).
func EncryptAESGCM(passphrase, plaintext string) ([]byte, error) {
	if passphrase == "" {
		return nil, errors.New("security: empty encryption passphrase")
	}
	key := sha256.Sum256([]byte(passphrase))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("security: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("security: cipher.NewGCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("security: nonce read: %w", err)
	}
	// Seal appends ciphertext+tag to nonce and returns the combined slice.
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// DecryptAESGCM is the inverse of EncryptAESGCM. It expects the same
// concatenated layout: nonce(12) || ciphertext || tag(16).
func DecryptAESGCM(passphrase string, blob []byte) (string, error) {
	if passphrase == "" {
		return "", errors.New("security: empty encryption passphrase")
	}
	if len(blob) < 12+16 {
		return "", errors.New("security: ciphertext too short")
	}
	key := sha256.Sum256([]byte(passphrase))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", fmt.Errorf("security: aes.NewCipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("security: cipher.NewGCM: %w", err)
	}
	nonce, ct := blob[:gcm.NonceSize()], blob[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("security: gcm.Open: %w", err)
	}
	return string(pt), nil
}

// Fingerprint returns a short, non-reversible identifier for a ciphertext
// so audit logs can detect "this key changed" without ever logging plaintext.
// Returns the first 16 hex chars of SHA-256(blob) — that's 8 bytes of
// entropy, enough to detect any single-byte change in the ciphertext.
func Fingerprint(blob []byte) string {
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:8])
}
