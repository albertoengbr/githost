package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/store"
)

// TestHTTPSClonePushPull is the F1 acceptance gate over Smart HTTP:
// create a repository in GitHost, clone it with a real git client,
// commit, push, pull — and verify every authorization boundary.
func TestHTTPSClonePushPull(t *testing.T) {
	s := startServer(t)
	base := s.url()

	// Cast: alice owns the private repo; bob is a read-only member;
	// carol is a stranger with valid credentials.
	alice, aliceToken := s.userWithToken(auth.ScopeRepoRead + " " + auth.ScopeRepoWrite)
	bob, bobToken := s.userWithToken(auth.ScopeRepoRead + " " + auth.ScopeRepoWrite)
	carol, carolToken := s.userWithToken(auth.ScopeRepoRead + " " + auth.ScopeRepoWrite)
	repo := s.createRepo(alice, "proj", false)
	publicRepo := s.createRepo(alice, "open", true)
	ctx := t.Context()
	if err := s.store.AddMember(ctx, repo.ID, mustUser(t, s, bob).ID, store.PermissionRead); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()

	// 1. Anonymous clone of a private repository is rejected.
	cloneDir := filepath.Join(work, "anon")
	wantGitFail(t, work, "could not read Username", "clone", base+"/"+alice+"/proj.git", cloneDir)

	// 2. Anonymous clone of a nonexistent repository also challenges
	// (existence must not leak to unauthenticated clients).
	wantGitFail(t, work, "could not read Username", "clone", base+"/"+alice+"/ghost.git", filepath.Join(work, "ghost"))

	// 3. Anonymous clone of a public repository succeeds (no .git suffix
	// in the URL — the transport strips it either way).
	mustGit(t, work, "clone", base+"/"+alice+"/"+publicRepo.Name, filepath.Join(work, "pub"))

	// 4. Owner clones the private repo with the token.
	projURL := authURL(base, alice, aliceToken, alice+"/proj.git")
	proj := filepath.Join(work, "proj")
	mustGit(t, work, "clone", projURL, proj)

	// 5. Commit and push.
	writeFile(t, proj, "README.md", "# proj\nhello from e2e\n")
	mustGit(t, proj, "add", "README.md")
	mustGit(t, proj, "commit", "-m", "initial commit")
	mustGit(t, proj, "push", "origin", "main")

	// 6. Server-side repository is a healthy bare repo with the push.
	bare := s.barePath(repo)
	out := mustGit(t, bare, "rev-parse", "--verify", "refs/heads/main")
	if !strings.Contains(out, "initial commit") && len(strings.TrimSpace(out)) < 40 {
		t.Fatalf("unexpected rev-parse output: %q", out)
	}
	if out := mustGit(t, bare, "fsck", "--strict"); strings.Contains(out, "error") || strings.Contains(out, "missing") {
		t.Fatalf("git fsck reported problems:\n%s", out)
	}

	// 7. A second clone (owner, again) sees the pushed commit and a pull
	// brings in later changes.
	proj2 := filepath.Join(work, "proj2")
	mustGit(t, work, "clone", projURL, proj2)
	writeFile(t, proj2, "second.txt", "second change\n")
	mustGit(t, proj2, "add", "second.txt")
	mustGit(t, proj2, "commit", "-m", "second commit")
	mustGit(t, proj2, "push", "origin", "main")

	// Pull in the first working copy.
	writeFile(t, proj, "third.txt", "third change\n")
	mustGit(t, proj, "add", "third.txt")
	mustGit(t, proj, "commit", "-m", "third commit")
	mustGit(t, proj, "pull", "--rebase=false", "origin", "main")
	// After the pull, the merge brought the second change in.
	if _, err := os.Stat(filepath.Join(proj, "second.txt")); err != nil {
		t.Fatalf("second.txt not present after pull: %v", err)
	}
	mustGit(t, proj, "push", "origin", "main")

	// History integrity: fsck still clean after concurrent-style flows.
	if out := mustGit(t, bare, "fsck", "--strict"); strings.Contains(out, "error") {
		t.Fatalf("git fsck after pulls reported problems:\n%s", out)
	}

	// 8. Read-only member can clone...
	projBob := filepath.Join(work, "proj-bob")
	mustGit(t, work, "clone", authURL(base, bob, bobToken, alice+"/proj.git"), projBob)
	// ...but cannot push.
	writeFile(t, projBob, "sneaky.txt", "should not land\n")
	mustGit(t, projBob, "add", "sneaky.txt")
	mustGit(t, projBob, "commit", "-m", "sneaky commit")
	wantGitFail(t, projBob, "", "push", "origin", "main")
	// The rejected push must not have modified the server repository.
	if out := mustGit(t, bare, "log", "--format=%s", "refs/heads/main"); strings.Contains(out, "sneaky") {
		t.Fatal("rejected push leaked into the repository")
	}

	// 9. A stranger with valid credentials gets 404 (existence hidden).
	wantGitFail(t, work, "not found", "clone",
		authURL(base, carol, carolToken, alice+"/proj.git"),
		filepath.Join(work, "proj-carol"))

	// 10. A read-only member clones fine, but pushing yields 403.
	reader, readerToken := s.userWithToken(auth.ScopeRepoRead)
	if err := s.store.AddMember(ctx, repo.ID, mustUser(t, s, reader).ID, store.PermissionRead); err != nil {
		t.Fatal(err)
	}
	projReader := filepath.Join(work, "proj-reader")
	mustGit(t, work, "clone", authURL(base, reader, readerToken, alice+"/proj.git"), projReader)
	writeFile(t, projReader, "reader.txt", "attempt\n")
	mustGit(t, projReader, "add", "reader.txt")
	mustGit(t, projReader, "commit", "-m", "reader attempts a write")
	wantGitFail(t, projReader, "403", "push", "origin", "main")

	// 11. Anonymous push to a public repository is rejected.
	pubURL := base + "/" + alice + "/" + publicRepo.Name
	pub := filepath.Join(work, "pub2")
	mustGit(t, work, "clone", pubURL, pub)
	writeFile(t, pub, "anon.txt", "anonymous write attempt\n")
	mustGit(t, pub, "add", "anon.txt")
	mustGit(t, pub, "commit", "-m", "anon write")
	wantGitFail(t, pub, "could not read Username", "push", "origin", "main")

	// 12. Protocol v2 works end-to-end (GIT_PROTOCOL passthrough).
	projV2 := filepath.Join(work, "proj-v2")
	mustGit(t, work, "-c", "protocol.version=2", "clone", projURL, projV2)
}

// TestHTTPSArchivedRepoDeniesPush verifies the archived flag on the wire.
func TestHTTPSArchivedRepoDeniesPush(t *testing.T) {
	s := startServer(t)
	base := s.url()
	owner, token := s.userWithToken(auth.ScopeRepoRead + " " + auth.ScopeRepoWrite)
	repo := s.createRepo(owner, "frozen", false)
	ctx := t.Context()
	if err := s.reposvc.SetArchived(ctx, repo, true); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	dir := filepath.Join(work, "frozen")
	mustGit(t, work, "clone", authURL(base, owner, token, owner+"/frozen.git"), dir)
	writeFile(t, dir, "x.txt", "x\n")
	mustGit(t, dir, "add", "x.txt")
	mustGit(t, dir, "commit", "-m", "x")
	wantGitFail(t, dir, "archived", "push", "origin", "main")

	// Reads still work while archived.
	mustGit(t, dir, "fetch", "origin")
}

// TestHTTPSPathTraversalProbes verifies hostile repository paths. Note
// the two layers involved:
//
//   - libcurl (the git client) resolves dot segments BEFORE sending, so
//     "owner/./../owner/repo" arrives as the clean "owner/repo" and is
//     then subject to normal authorization — it cannot grant anything the
//     clean URL wouldn't;
//   - raw dot-segment requests (clients without normalization, or
//     percent-encoded) are rejected with 400 by httpgit.Guard.
func TestHTTPSPathTraversalProbes(t *testing.T) {
	s := startServer(t)
	base := s.url()
	owner, token := s.userWithToken(auth.ScopeRepoRead)
	s.createRepo(owner, "real", false)
	stranger, strangerToken := s.userWithToken(auth.ScopeRepoRead)

	work := t.TempDir()

	// 1. Raw dot-segment requests are rejected with 400 by the guard.
	// Go's HTTP client, unlike libcurl, does not normalize dot segments,
	// so these reach the server exactly as written.
	rawProbes := []string{
		"/" + owner + "/./../" + owner + "/real/info/refs?service=git-upload-pack",
		"/" + owner + "/%2e%2e/" + owner + "/real/info/refs?service=git-upload-pack",
		"/../" + owner + "/real/info/refs?service=git-upload-pack",
		"/" + owner + "/real/../../../../../etc/passwd/info/refs",
	}
	for _, probe := range rawProbes {
		req, err := http.NewRequest(http.MethodGet, base+probe, nil)
		if err != nil {
			t.Fatalf("probe %q: %v", probe, err)
		}
		req.SetBasicAuth(owner, token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("probe %q: %v", probe, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("probe %q: status = %d, want 400 (guard should reject dot segments)", probe, resp.StatusCode)
		}
	}

	// 2. Client-normalized traversal must not grant access beyond the
	// clean URL: a stranger using owner-scoped dot segments is still a
	// stranger on the resolved repository.
	wantGitFail(t, work, "not found", "ls-remote",
		authURL(base, stranger, strangerToken, owner+"/./../"+owner+"/real"))

	// The owner using the same shape reaches their own repository —
	// equivalent to the clean URL.
	mustGit(t, work, "ls-remote", authURL(base, owner, token, owner+"/./../"+owner+"/real"))

	// 3. The repositories root must contain exactly the one legitimate
	// repository — no probe created anything outside it.
	entries, err := os.ReadDir(s.repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("repositories root has %d entries, want exactly 1 (the created repo)", len(entries))
	}
	if !strings.HasSuffix(entries[0].Name(), ".git") {
		t.Fatalf("unexpected entry %q in repositories root", entries[0].Name())
	}
}

func mustUser(t *testing.T, s *server, username string) *store.User {
	t.Helper()
	user, err := s.store.UserByUsername(t.Context(), username)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
