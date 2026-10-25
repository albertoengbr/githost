package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/store"

	"golang.org/x/crypto/ssh"
)

// runForTestWithInput is runForTest with a caller-supplied stdin, used by
// commands that read a password from standard input.
func runForTestWithInput(t *testing.T, input string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, strings.NewReader(input), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

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

func TestAdminSetPassword(t *testing.T) {
	cfgPath := initInstance(t)
	if code, _, stderr := runForTest(t, "admin", "create-user",
		"--config", cfgPath, "--username", "alice", "--password-stdin"); code != 0 {
		t.Fatalf("create-user: %s", stderr)
	}

	// A live session must not survive the password change.
	ctx := context.Background()
	in, err := openInstance(ctx, cfgPath)
	if err != nil {
		t.Fatalf("open instance: %v", err)
	}
	alice, err := in.st.UserByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("lookup alice: %v", err)
	}
	if err := in.st.CreateSession(ctx, "session-under-test", alice.ID,
		time.Now().Add(time.Hour), "127.0.0.1", "test"); err != nil {
		t.Fatalf("create session: %v", err)
	}
	in.Close()

	// Username lookup is case-insensitive; the password comes from stdin.
	code, stdout, stderr := runForTestWithInput(t, "brand-new-password-9\n",
		"admin", "set-password", "--config", cfgPath,
		"--username", "Alice", "--password-stdin")
	if code != 0 {
		t.Fatalf("set-password exit=%d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "alice") || !strings.Contains(stdout, "sessions invalidated") {
		t.Fatalf("unexpected output: %q", stdout)
	}

	in, err = openInstance(ctx, cfgPath)
	if err != nil {
		t.Fatalf("reopen instance: %v", err)
	}
	defer in.Close()
	alice, err = in.st.UserByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("lookup alice again: %v", err)
	}
	if ok, err := auth.VerifyPassword("brand-new-password-9", alice.PasswordHash); err != nil || !ok {
		t.Fatalf("new password does not verify: ok=%v err=%v", ok, err)
	}
	if ok, _ := auth.VerifyPassword(strings.TrimSpace(testPassword), alice.PasswordHash); ok {
		t.Fatal("old password still verifies after set-password")
	}
	if _, err := in.st.SessionByHash(ctx, "session-under-test"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session survived the password change: err=%v", err)
	}
}

func TestAdminSetPasswordErrors(t *testing.T) {
	cfgPath := initInstance(t)
	if code, _, stderr := runForTest(t, "admin", "create-user",
		"--config", cfgPath, "--username", "alice", "--password-stdin"); code != 0 {
		t.Fatalf("create-user: %s", stderr)
	}

	// Password policy failure.
	code, _, stderr := runForTestWithInput(t, "short\n",
		"admin", "set-password", "--config", cfgPath,
		"--username", "alice", "--password-stdin")
	if code != 1 || !strings.Contains(stderr, "password") {
		t.Fatalf("short password exit=%d stderr=%q", code, stderr)
	}

	// Unknown account.
	code, _, _ = runForTestWithInput(t, "brand-new-password-9\n",
		"admin", "set-password", "--config", cfgPath,
		"--username", "ghost", "--password-stdin")
	if code != 1 {
		t.Fatalf("unknown user exit=%d, want 1", code)
	}

	// Missing password source is a usage error.
	code, _, _ = runForTest(t, "admin", "set-password",
		"--config", cfgPath, "--username", "alice")
	if code != 2 {
		t.Fatalf("missing password source exit=%d, want 2", code)
	}

	// Missing username is a usage error.
	code, _, _ = runForTest(t, "admin", "set-password", "--config", cfgPath)
	if code != 2 {
		t.Fatalf("missing username exit=%d, want 2", code)
	}

	// --password also works (the discouraged non-interactive path).
	code, _, stderr = runForTest(t, "admin", "set-password",
		"--config", cfgPath, "--username", "alice",
		"--password", "another-good-password-7")
	if code != 0 {
		t.Fatalf("--password exit=%d: %s", code, stderr)
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
