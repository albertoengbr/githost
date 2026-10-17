package security

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndLoadSecretFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")

	if err := WriteSecretFile(path); err != nil {
		t.Fatalf("WriteSecretFile: %v", err)
	}
	secret, err := LoadSecretFile(path)
	if err != nil {
		t.Fatalf("LoadSecretFile: %v", err)
	}
	if len(secret) != 2*SecretBytes {
		t.Fatalf("secret length = %d, want %d", len(secret), 2*SecretBytes)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("secret file missing: %v", err)
	}

	// A second write must refuse to replace the existing secret.
	if err := WriteSecretFile(path); err == nil {
		t.Fatal("second WriteSecretFile must fail")
	}

	// Round-trip: the loaded value must not change.
	secret2, err := LoadSecretFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if secret != secret2 {
		t.Fatal("secret changed between reads")
	}
}

func TestLoadSecretFileRejectsShort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecretFile(path); err == nil {
		t.Fatal("want error for short secret")
	}
}
