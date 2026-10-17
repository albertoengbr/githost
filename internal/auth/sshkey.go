package auth

import (
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ParseSSHPublicKey validates a public key in authorized_keys format
// ("<type> <base64> [comment]"). It returns the canonical single-line form
// (comment stripped) and the SHA256 fingerprint used as the storage key.
func ParseSSHPublicKey(line string) (canonical string, fingerprint string, err error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(line)))
	if err != nil {
		return "", "", fmt.Errorf("auth: parse public key: %w", err)
	}
	canonical = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	return canonical, ssh.FingerprintSHA256(pub), nil
}

// FingerprintSSHPublicKey returns the SHA256 fingerprint of a canonical
// public key line.
func FingerprintSSHPublicKey(canonical string) (string, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(canonical)))
	if err != nil {
		return "", fmt.Errorf("auth: parse public key: %w", err)
	}
	return ssh.FingerprintSHA256(pub), nil
}
