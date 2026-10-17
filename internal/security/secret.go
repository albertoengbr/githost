// Package security provides credential and secret handling primitives.
package security

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecretBytes is the amount of entropy (in bytes) of generated instance
// secrets; the hex-encoded form is 2 * SecretBytes characters long.
const SecretBytes = 32

// GenerateSecret returns a hex-encoded random secret.
func GenerateSecret() (string, error) {
	buf := make([]byte, SecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("read random source: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// WriteSecretFile generates a new secret and stores it at path with
// restrictive permissions (0600 on POSIX systems). It refuses to overwrite
// an existing file.
func WriteSecretFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("secret file %s already exists", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect secret file %s: %w", path, err)
	}
	secret, err := GenerateSecret()
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create secret directory: %w", err)
		}
	}
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return fmt.Errorf("write secret file: %w", err)
	}
	return nil
}

// LoadSecretFile reads and sanity-checks the instance secret.
func LoadSecretFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret file: %w", err)
	}
	secret := strings.TrimSpace(string(raw))
	if len(secret) < 2*SecretBytes {
		return "", fmt.Errorf("secret file %s: too short (%d chars, want %d)", path, len(secret), 2*SecretBytes)
	}
	return secret, nil
}
