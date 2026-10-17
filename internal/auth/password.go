// Package auth implements credential handling: Argon2id password hashing,
// access-token generation with scopes, and SSH public-key parsing. It is the
// only package that transforms secrets; storage keeps nothing but derived
// values (hashes, fingerprints).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters, following the OWASP Password Storage Cheat Sheet
// baseline (m=19 MiB, t=2, p=1 for typical servers).
const (
	argon2Time    = 2
	argon2Memory  = 19 * 1024 // KiB
	argon2Threads = 1
	argon2KeyLen  = 32
	argon2SaltLen = 16

	argon2MaxMemory  = 1 << 22 // sanity ceiling for parsed hashes (4 GiB)
	argon2MaxTime    = 8
	argon2MaxThreads = 8
)

// HashPassword derives a PHC-encoded Argon2id hash for the password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2Memory, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether the password matches a PHC-encoded
// Argon2id hash. Malformed or exotic parameter sets are rejected rather
// than trusted, and comparison is constant-time.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, fmt.Errorf("auth: not an argon2id PHC hash")
	}
	if !strings.HasPrefix(parts[2], "v=") {
		return false, fmt.Errorf("auth: missing argon2 version")
	}
	version, err := strconv.Atoi(strings.TrimPrefix(parts[2], "v="))
	if err != nil || version != argon2.Version {
		return false, fmt.Errorf("auth: unsupported argon2 version %q", parts[2])
	}
	memory, timeIter, threads, err := parseArgon2Params(parts[3])
	if err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, fmt.Errorf("auth: bad salt encoding")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, fmt.Errorf("auth: bad key encoding")
	}
	got := argon2.IDKey([]byte(password), salt, timeIter, memory, uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func parseArgon2Params(s string) (uint32, uint32, uint32, error) {
	var memory, timeIter, threads uint32
	for _, kv := range strings.Split(s, ",") {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			return 0, 0, 0, fmt.Errorf("auth: bad argon2 parameter %q", kv)
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("auth: bad argon2 parameter value %q", kv)
		}
		switch key {
		case "m":
			memory = uint32(n)
		case "t":
			timeIter = uint32(n)
		case "p":
			threads = uint32(n)
		default:
			return 0, 0, 0, fmt.Errorf("auth: unknown argon2 parameter %q", key)
		}
	}
	if memory < 8*1024 || memory > argon2MaxMemory || timeIter < 1 || timeIter > argon2MaxTime || threads < 1 || threads > argon2MaxThreads {
		return 0, 0, 0, fmt.Errorf("auth: argon2 parameters out of accepted range")
	}
	return memory, timeIter, threads, nil
}

// ValidatePasswordPolicy enforces the minimum credential quality accepted by
// GitHost. The upper bound protects the KDF from abuse via giant inputs.
func ValidatePasswordPolicy(password string) error {
	if len(password) < 8 {
		return fmt.Errorf("password: must be at least 8 characters")
	}
	if len(password) > 512 {
		return fmt.Errorf("password: must be at most 512 characters")
	}
	return nil
}
