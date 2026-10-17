package e2e

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/store"
)

// sshOpts builds the ssh command options for git and raw ssh probes.
// Paths use forward slashes: git tokenizes GIT_SSH_COMMAND itself and
// eats backslashes (it does not go through a shell).
func sshOpts(keyPath string) []string {
	nullPath := "/dev/null"
	if runtime.GOOS == "windows" {
		nullPath = "NUL"
	}
	keyPath = filepath.ToSlash(keyPath)
	return []string{
		"-i", keyPath,
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + nullPath,
		"-o", "BatchMode=yes",
	}
}

// sshGitURL builds ssh://git@host:port/owner/repo.git.
func sshGitURL(sshHost, owner, name string) string {
	return "ssh://git@" + sshHost + "/" + owner + "/" + name + ".git"
}

// clientSSH runs a raw ssh command (used to probe the exec policy).
// stdin is closed immediately: probes like a bare `git-upload-pack` would
// otherwise block waiting for a protocol conversation that never comes.
func clientSSH(t *testing.T, sshHost, keyPath string, command string) (string, error) {
	t.Helper()
	host, port, err := net.SplitHostPort(sshHost)
	if err != nil {
		t.Fatalf("bad ssh host %q: %v", sshHost, err)
	}
	args := append(sshOpts(keyPath), "-p", port, "git@"+host, command)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// generateClientKey creates an ed25519 keypair for a test "user machine",
// returning the private key path and the authorized_keys public line.
func generateClientKey(t *testing.T, dir string) (string, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "e2e client key")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Native Windows OpenSSH refuses private keys that inherited
		// group ACEs from the temp directory ("UNPROTECTED PRIVATE KEY
		// FILE"). Restrict the ACL to the current user, exactly like
		// ssh-keygen does with the keys it writes.
		user := os.Getenv("USERNAME")
		if user != "" {
			cmd := exec.Command("icacls", keyPath, "/inheritance:r", "/grant:r", user+":F")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Logf("icacls on test key: %v: %s", err, out)
			}
		}
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return keyPath, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
}

// registerSSHKey adds a public key for the given user.
func (s *server) registerSSHKey(t *testing.T, username, publicLine string) {
	t.Helper()
	canonical, fingerprint, err := auth.ParseSSHPublicKey(publicLine)
	if err != nil {
		t.Fatal(err)
	}
	user := mustUser(t, s, username)
	ctx := t.Context()
	if _, err := s.store.AddSSHKey(ctx, user.ID, fingerprint, canonical, "e2e"); err != nil {
		t.Fatal(err)
	}
}

// TestSSHClonePushPull is the F1 acceptance gate over SSH: register a key,
// clone, commit, push, pull — plus the negative policy checks.
func TestSSHClonePushPull(t *testing.T) {
	s := startServer(t)
	if s.sshAddr == "" {
		t.Fatal("SSH transport did not start")
	}
	work := t.TempDir()

	alice, _ := s.userWithToken("repo:read repo:write")
	repo := s.createRepo(alice, "proj", false)
	s.createRepo(alice, "open", true)

	keyPath, pubLine := generateClientKey(t, filepath.Join(work, "alice-machine"))
	s.registerSSHKey(t, alice, pubLine)

	projURL := sshGitURL(s.sshAddr, alice, "proj")

	// 1. A key that is not registered cannot even authenticate.
	otherKey, _ := generateClientKey(t, filepath.Join(work, "stranger-machine"))
	out, err := clientSSH(t, s.sshAddr, otherKey, "git-upload-pack '"+alice+"/proj.git'")
	if err == nil {
		t.Fatalf("unregistered key was accepted:\n%s", out)
	}
	if !strings.Contains(out, "Permission denied (publickey)") && !strings.Contains(out, "permission denied") {
		t.Fatalf("unexpected rejection output: %q", out)
	}

	// 2. Registered key clones the private repository.
	proj := filepath.Join(work, "proj")
	env := map[string]string{"GIT_SSH_COMMAND": "ssh " + strings.Join(sshOpts(keyPath), " ")}
	gitWithEnv(t, env, work, "clone", projURL, proj)

	// 3. Commit and push over SSH.
	writeFile(t, proj, "README.md", "# ssh proj\n")
	gitWithEnv(t, env, proj, "add", "README.md")
	gitWithEnv(t, env, proj, "commit", "-m", "initial ssh commit")
	gitWithEnv(t, env, proj, "push", "origin", "main")

	// 4. Second clone and pull.
	proj2 := filepath.Join(work, "proj2")
	gitWithEnv(t, env, work, "clone", projURL, proj2)
	writeFile(t, proj2, "second.txt", "second\n")
	gitWithEnv(t, env, proj2, "add", "second.txt")
	gitWithEnv(t, env, proj2, "commit", "-m", "second ssh commit")
	gitWithEnv(t, env, proj2, "push", "origin", "main")
	gitWithEnv(t, env, proj, "pull", "--rebase=false", "origin", "main")
	if _, err := os.Stat(filepath.Join(proj, "second.txt")); err != nil {
		t.Fatalf("second.txt not present after pull: %v", err)
	}

	// 5. Server-side integrity after SSH traffic.
	bare := s.barePath(repo)
	if out := mustGit(t, bare, "fsck", "--strict"); strings.Contains(out, "error") {
		t.Fatalf("git fsck reported problems:\n%s", out)
	}

	// 6. Shell commands are categorically refused.
	out, err = clientSSH(t, s.sshAddr, keyPath, "whoami")
	if err == nil {
		t.Fatalf("shell command was accepted:\n%s", out)
	}
	if !strings.Contains(out, "interactive sessions are disabled") && !strings.Contains(out, "only 'git-upload-pack'") {
		t.Fatalf("unexpected shell rejection output: %q", out)
	}

	// 7. Any other exec command shape is refused.
	out, err = clientSSH(t, s.sshAddr, keyPath, "rm -rf /")
	if err == nil {
		t.Fatalf("arbitrary command was accepted:\n%s", out)
	}

	// 8. A registered key from a stranger gets the not-found answer for
	// private repositories (existence hidden, like HTTP).
	stranger, _ := s.userWithToken("repo:read")
	strangerKey, strangerPub := generateClientKey(t, filepath.Join(work, "stranger-machine-2"))
	s.registerSSHKey(t, stranger, strangerPub)
	out, err = clientSSH(t, s.sshAddr, strangerKey, "git-upload-pack '"+alice+"/proj.git'")
	if err == nil {
		t.Fatalf("stranger could access a private repository:\n%s", out)
	}
	if !strings.Contains(out, "repository not found") {
		t.Fatalf("unexpected stranger rejection output: %q", out)
	}

	// 9. Anonymous fetch of a public repository via SSH: any registered
	// key can read a public repository.
	out, err = clientSSH(t, s.sshAddr, strangerKey, "git-upload-pack '"+alice+"/open.git'")
	if err != nil && !strings.Contains(out, "repository not found") {
		// upload-pack without a client conversation exits non-zero after
		// printing the advertisement; the absence of "not found" means
		// the advertisement was served.
		t.Logf("public fetch probe output: %q", out)
	} else if strings.Contains(out, "repository not found") {
		t.Fatalf("public repository should be readable: %q", out)
	}

	// 10. Read-only policy: a read member cannot push over SSH.
	reader, _ := s.userWithToken("repo:read")
	readerKey, readerPub := generateClientKey(t, filepath.Join(work, "reader-machine"))
	s.registerSSHKey(t, reader, readerPub)
	ctx := t.Context()
	if err := s.store.AddMember(ctx, repo.ID, mustUser(t, s, reader).ID, store.PermissionRead); err != nil {
		t.Fatal(err)
	}
	readerDir := filepath.Join(work, "proj-reader")
	readerURL := sshGitURL(s.sshAddr, alice, "proj")
	gitWithEnv(t, map[string]string{"GIT_SSH_COMMAND": "ssh " + strings.Join(sshOpts(readerKey), " ")},
		work, "clone", readerURL, readerDir)
	writeFile(t, readerDir, "reader.txt", "x\n")
	gitWithEnv(t, map[string]string{"GIT_SSH_COMMAND": "ssh " + strings.Join(sshOpts(readerKey), " ")},
		readerDir, "add", "reader.txt")
	gitWithEnv(t, map[string]string{"GIT_SSH_COMMAND": "ssh " + strings.Join(sshOpts(readerKey), " ")},
		readerDir, "commit", "-m", "reader write attempt")
	envReader := map[string]string{"GIT_SSH_COMMAND": "ssh " + strings.Join(sshOpts(readerKey), " ")}
	out, err = gitWithEnvOutput(t, envReader, readerDir, "push", "origin", "main")
	if err == nil {
		t.Fatalf("read member pushed successfully:\n%s", out)
	}
	if !strings.Contains(out, "permission denied") && !strings.Contains(out, "pre-receive hook declined") {
		t.Fatalf("unexpected push rejection: %q", out)
	}

	// The rejected push must not have modified the repository.
	if out := mustGit(t, bare, "log", "--format=%s", "refs/heads/main"); strings.Contains(out, "reader write") {
		t.Fatal("rejected SSH push leaked into the repository")
	}
}

// gitWithEnv runs client git with extra environment variables.
func gitWithEnv(t *testing.T, extraEnv map[string]string, dir string, args ...string) {
	t.Helper()
	out, err := gitWithEnvOutput(t, extraEnv, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// gitWithEnvOutput runs client git with extra environment variables,
// returning output and error without failing the test.
func gitWithEnvOutput(t *testing.T, extraEnv map[string]string, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=E2E Tester",
		"GIT_AUTHOR_EMAIL=e2e@example.test",
		"GIT_COMMITTER_NAME=E2E Tester",
		"GIT_COMMITTER_EMAIL=e2e@example.test",
	)
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}
