package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// initInstance runs `githost init` against a fresh directory and returns
// the config path.
func initInstance(t *testing.T) string {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "githost.yaml")
	code, _, stderr := runForTest(t, "init", "--config", cfgPath)
	if code != 0 {
		t.Fatalf("init failed: %s", stderr)
	}
	return cfgPath
}

func TestAdminCreateUserAndFirstUserIsAdmin(t *testing.T) {
	cfgPath := initInstance(t)

	code, stdout, stderr := runForTest(t, "admin", "create-user",
		"--config", cfgPath, "--username", "alice", "--password-stdin")
	if code != 0 {
		t.Fatalf("create-user exit=%d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "admin=true") {
		t.Fatalf("first user must be an admin: %q", stdout)
	}

	// Second user is a regular account.
	code, stdout, _ = runForTest(t, "admin", "create-user",
		"--config", cfgPath, "--username", "Bob", "--password-stdin")
	if code != 0 {
		t.Fatalf("second create-user failed: %s", stdout)
	}
	if strings.Contains(stdout, "admin=true") {
		t.Fatalf("second user must not be admin: %q", stdout)
	}

	// Usernames are normalized to lowercase; a duplicate must fail.
	code, _, _ = runForTest(t, "admin", "create-user",
		"--config", cfgPath, "--username", "alice", "--password-stdin")
	if code == 0 {
		t.Fatal("duplicate username accepted")
	}

	// Listing shows both users.
	code, stdout, _ = runForTest(t, "admin", "list-users", "--config", cfgPath)
	if code != 0 || !strings.Contains(stdout, "alice") || !strings.Contains(stdout, "bob") {
		t.Fatalf("list-users: %q", stdout)
	}
}

func TestAdminCreateUserNeedsStdin(t *testing.T) {
	cfgPath := initInstance(t)
	// No --password/--password-stdin → usage error.
	code, _, _ := runForTest(t, "admin", "create-user",
		"--config", cfgPath, "--username", "alice")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestAdminCreateToken(t *testing.T) {
	cfgPath := initInstance(t)
	if code, _, stderr := runForTest(t, "admin", "create-user",
		"--config", cfgPath, "--username", "alice", "--password-stdin"); code != 0 {
		t.Fatalf("create-user: %s", stderr)
	}

	code, stdout, stderr := runForTest(t, "admin", "create-token",
		"--config", cfgPath, "--username", "alice",
		"--scope", "repo:read", "--scope", "repo:write")
	if code != 0 {
		t.Fatalf("create-token exit=%d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "ght_") {
		t.Fatalf("token not printed: %q", stdout)
	}

	// Unknown scope rejected.
	code, _, _ = runForTest(t, "admin", "create-token",
		"--config", cfgPath, "--username", "alice", "--scope", "root")
	if code != 1 {
		t.Fatalf("bad scope exit = %d, want 1", code)
	}

	// Unknown user rejected.
	code, _, _ = runForTest(t, "admin", "create-token",
		"--config", cfgPath, "--username", "ghost", "--scope", "repo:read")
	if code != 1 {
		t.Fatalf("unknown user exit = %d, want 1", code)
	}
}

func TestAdminAddSSHKey(t *testing.T) {
	cfgPath := initInstance(t)
	if code, _, stderr := runForTest(t, "admin", "create-user",
		"--config", cfgPath, "--username", "alice", "--password-stdin"); code != 0 {
		t.Fatalf("create-user: %s", stderr)
	}

	// A real key, written to a file.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(keyFile, ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runForTest(t, "admin", "add-ssh-key",
		"--config", cfgPath, "--username", "alice", "--key-file", keyFile, "--title", "laptop")
	if code != 0 {
		t.Fatalf("add-ssh-key exit=%d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "SHA256:") {
		t.Fatalf("fingerprint not printed: %q", stdout)
	}

	// Invalid key material is rejected.
	code, _, _ = runForTest(t, "admin", "add-ssh-key",
		"--config", cfgPath, "--username", "alice", "--key", "garbage")
	if code != 1 {
		t.Fatalf("invalid key exit = %d, want 1", code)
	}
}

func TestRepoLifecycle(t *testing.T) {
	cfgPath := initInstance(t)
	runForTest(t, "admin", "create-user", "--config", cfgPath, "--username", "alice", "--password-stdin")
	runForTest(t, "admin", "create-user", "--config", cfgPath, "--username", "bob", "--password-stdin")

	code, stdout, stderr := runForTest(t, "repo", "create",
		"--config", cfgPath, "--owner", "alice", "--name", "my-project")
	if code != 0 {
		t.Fatalf("repo create exit=%d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "(private") {
		t.Fatalf("repo must default to private: %q", stdout)
	}

	// Public visibility.
	if code, _, _ := runForTest(t, "repo", "create",
		"--config", cfgPath, "--owner", "alice", "--name", "open", "--visibility", "public"); code != 0 {
		t.Fatal("public repo create failed")
	}

	// Invalid name.
	if code, _, _ := runForTest(t, "repo", "create",
		"--config", cfgPath, "--owner", "alice", "--name", "CON"); code != 1 {
		t.Fatal("reserved name accepted")
	}

	// Membership.
	if code, _, stderr := runForTest(t, "repo", "add-member",
		"--config", cfgPath, "--owner", "alice", "--name", "my-project",
		"--user", "bob", "--permission", "write"); code != 0 {
		t.Fatalf("add-member: %s", stderr)
	}
	if code, _, _ := runForTest(t, "repo", "add-member",
		"--config", cfgPath, "--owner", "alice", "--name", "my-project",
		"--user", "bob", "--permission", "superuser"); code != 1 {
		t.Fatal("bad permission accepted")
	}
	if code, _, _ := runForTest(t, "repo", "remove-member",
		"--config", cfgPath, "--owner", "alice", "--name", "my-project", "--user", "bob"); code != 0 {
		t.Fatal("remove-member failed")
	}

	// Listing.
	code, stdout, _ = runForTest(t, "repo", "list", "--config", cfgPath, "--owner", "alice")
	if code != 0 || !strings.Contains(stdout, "my-project") || !strings.Contains(stdout, "open") {
		t.Fatalf("repo list: %q", stdout)
	}

	// Deletion requires --confirm...
	if code, _, _ := runForTest(t, "repo", "delete",
		"--config", cfgPath, "--owner", "alice", "--name", "open"); code != 2 {
		t.Fatal("delete without --confirm must be a usage error")
	}
	// ...and then works.
	if code, _, stderr := runForTest(t, "repo", "delete",
		"--config", cfgPath, "--owner", "alice", "--name", "open", "--confirm"); code != 0 {
		t.Fatalf("repo delete: %s", stderr)
	}
	if code, stdout, _ := runForTest(t, "repo", "list", "--config", cfgPath, "--owner", "alice"); code != 0 || strings.Contains(stdout, "open\t") {
		t.Fatalf("repo still listed after delete: %q", stdout)
	}
}

// runForTest pipes a fixed password via the injected stdin (see
// cli_test.go), so `--password-stdin` is exercised end-to-end here.
