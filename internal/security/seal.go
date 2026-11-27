package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// SealPrefix marks values sealed with Seal. It lets a data migration tell
// already-protected rows from legacy plaintext without a schema column.
const SealPrefix = "ghs1:"

// sealKey derives a 32-byte key from the high-entropy instance secret.
// Domain separation keeps a sealed webhook secret distinct from any other
// value derived from the same secret (TOTP, CSRF, sessions).
func sealKey(instanceSecret string) []byte {
	h := sha256.Sum256([]byte("githost-seal:" + instanceSecret))
	return h[:]
}

// Seal encrypts a secret with AES-256-GCM under the instance secret. The
// result is self-describing ("ghs1:<hex>") with a fresh random nonce on
// every call, so identical plaintexts never produce identical stored values.
func Seal(plaintext, instanceSecret string) (string, error) {
	block, err := aes.NewCipher(sealKey(instanceSecret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("seal: random nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return SealPrefix + hex.EncodeToString(sealed), nil
}

// Open reverses Seal. Values without the prefix are rejected: callers that
// still hold legacy plaintext must migrate them explicitly, never silently
// treat them as authenticated ciphertext.
func Open(sealed, instanceSecret string) (string, error) {
	if !IsSealed(sealed) {
		return "", errors.New("security: value is not sealed")
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(sealed, SealPrefix))
	if err != nil {
		return "", fmt.Errorf("security: bad sealed value: %w", err)
	}
	block, err := aes.NewCipher(sealKey(instanceSecret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("security: sealed value too short")
	}
	plaintext, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("security: sealed value failed authentication")
	}
	return string(plaintext), nil
}

// IsSealed reports whether a stored value is in the sealed format.
func IsSealed(value string) bool {
	return strings.HasPrefix(value, SealPrefix)
}
