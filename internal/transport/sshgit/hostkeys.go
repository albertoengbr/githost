package sshgit

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// loadOrGenerateHostKey returns the ed25519 host key for this instance,
// generating and persisting it (0600) on first use. Keys live under
// <data>/ssh/ and can be rotated by stopping the server and replacing
// the file.
func loadOrGenerateHostKey(dir string) (ssh.Signer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create host key directory: %w", err)
	}
	path := filepath.Join(dir, "host_ed25519")

	data, err := os.ReadFile(path)
	if err == nil {
		return ssh.ParsePrivateKey(data)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "githost ssh host key")
	if err != nil {
		return nil, fmt.Errorf("encode host key: %w", err)
	}
	encoded := pem.EncodeToMemory(block)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("persist host key: %w", err)
	}
	return ssh.ParsePrivateKey(encoded)
}
