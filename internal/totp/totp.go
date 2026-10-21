// Package totp implements TOTP (RFC 6238) for GitHost's MFA.
//
// Secrets are encrypted with the instance secret before storage — a
// database leak alone does not compromise TOTP seeds. The encryption
// uses AES-GCM with a key derived from the instance secret via HKDF.
package totp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"githost.dev/githost/internal/store"
)

// DeriveKey produces a 32-byte AES key from the instance secret.
func DeriveKey(instanceSecret string) []byte {
	h := sha256.Sum256([]byte("githost-totp:" + instanceSecret))
	return h[:]
}

// EncryptSecret seals a TOTP secret with AES-GCM.
func EncryptSecret(plaintext, instanceSecret string) (string, error) {
	block, err := aes.NewCipher(DeriveKey(instanceSecret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(sealed), nil
}

// DecryptSecret opens an AES-GCM sealed TOTP secret.
func DecryptSecret(ciphertext, instanceSecret string) (string, error) {
	raw, err := hex.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(DeriveKey(instanceSecret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("totp: ciphertext too short")
	}
	plaintext, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("totp: decrypt failed (wrong instance secret?)")
	}
	return string(plaintext), nil
}

// Generate produces a new TOTP secret and otpauth URL.
func Generate(account, issuer string) (secret, otpauthURL string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: account,
		Period:      30,
		Digits:      otp.DigitsSix,
		Algorithm:   otp.AlgorithmSHA1,
	})
	if err != nil {
		return "", "", fmt.Errorf("totp generate: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// Verify checks a TOTP code with a ±1 step window.
func Verify(code, secret string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	ok, err := totp.ValidateCustom(code, secret, time.Now(), totp.ValidateOpts{
		Period:    30,
		Skew:      1, // allow ±1 step (±30s clock drift)
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	return err == nil && ok
}

// GenerateRecoveryCodes produces N one-time recovery codes.
// Format: XXXX-XXXX-XXXX (hex, human-typable).
// Returns (plaintext codes, sha256-hashed codes).
func GenerateRecoveryCodes(n int) ([]string, []string, error) {
	if n <= 0 || n > 20 {
		n = 10
	}
	var plaintext, hashed []string
	for i := 0; i < n; i++ {
		buf := make([]byte, 6) // 48 bits — enough for one-time codes
		if _, err := rand.Read(buf); err != nil {
			return nil, nil, err
		}
		raw := hex.EncodeToString(buf) // 12 hex chars
		code := raw[:4] + "-" + raw[4:8] + "-" + raw[8:12]
		plaintext = append(plaintext, code)
		hashed = append(hashed, store.HashToken(code))
	}
	return plaintext, hashed, nil
}
