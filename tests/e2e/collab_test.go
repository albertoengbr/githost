package e2e

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/store"
)

// TestCollaborationWorkflow is the F4 acceptance gate: issue creation,
// PR lifecycle (create → review → merge), and verification that the
// merge landed in the repository.
func TestCollaborationWorkflow(t *testing.T) {
	s := startServer(t)
	base := s.url()
	ctx := context.Background()

	alice, _ := s.userWithToken(auth.ScopeRepoRead + " " + auth.ScopeRepoWrite)
	bob, _ := s.userWithToken(auth.ScopeRepoRead + " " + auth.ScopeRepoWrite)

	// Give both users real passwords for web login.
	alicePass := "collab-alice-pass-1"
	bobPass := "collab-bob-pass-1"
	ha, err := auth.HashPassword(alicePass)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetPassword(ctx, mustUser(t, s, alice).ID, ha); err != nil {
		t.Fatal(err)
	}
	hb, err := auth.HashPassword(bobPass)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetPassword(ctx, mustUser(t, s, bob).ID, hb); err != nil {
		t.Fatal(err)
	}

	s.createRepo(alice, "collab", false)

	repo, err := s.reposvc.Resolve(ctx, alice, "collab")
	if err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(s.repoDir, repo.UUID+".git")

	// Push main + dev branches with real content.
	work := t.TempDir()
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	runWork(t, work, "init", "--initial-branch=main")
	writeFile(t, work, "README.md", "# collab\n")
	runWork(t, work, "add", ".")
	runWork(t, work, "commit", "-m", "initial")
	runWork(t, work, "checkout", "-b", "feature")
	writeFile(t, work, "feature.txt", "feature work\n")
	runWork(t, work, "add", ".")
	runWork(t, work, "commit", "-m", "add feature")
	runWork(t, work, "push", filepath.ToSlash(bare), "main", "feature")

	// Give bob write access.
	if err := s.store.AddMember(ctx, repo.ID, mustUser(t, s, bob).ID, store.PermissionWrite); err != nil {
		t.Fatal(err)
	}

	// Login as alice (web session).
	jarA, _ := cookiejar.New(nil)
	clientA := &http.Client{Jar: jarA, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	loginWeb(t, clientA, base, alice, alicePass)

	// Login as bob.
	jarB, _ := cookiejar.New(nil)
	clientB := &http.Client{Jar: jarB, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	loginWeb(t, clientB, base, bob, bobPass)

	// ---- 1. Create an issue via the web form.
	form := url.Values{}
	form.Set("title", "Bug in feature")
	form.Set("body", "Something **broken** happens.")
	newIssueURL := "/" + alice + "/collab/issues/new"
	csrfA := getCSRF(t, clientA, base, newIssueURL)
	form.Set("csrf", csrfA)
	resp := postWeb(t, clientA, base, newIssueURL, form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("issue create: %d\nbody: %s", resp.StatusCode, firstN(resp.Body, 3000))
	}
	issueURL := resp.Header.Get("Location")
	if !strings.Contains(issueURL, "/issues/1") {
		t.Fatalf("issue redirect = %q", issueURL)
	}

	// Issue page renders with Markdown.
	page := getWeb(t, clientA, base, issueURL)
	// Debug: show the full page when the test fails.
	if !strings.Contains(page, "<strong>broken</strong>") {
		t.Fatalf("full page (%d bytes):\n%s", len(page), page)
	}
	if !strings.Contains(page, "Bug in feature") {
		t.Fatal("issue title missing")
	}

	// Bob comments on the issue.
	form = url.Values{}
	form.Set("body", "I can reproduce this.")
	form.Set("csrf", getCSRF(t, clientB, base, issueURL))
	resp = postWeb(t, clientB, base, issueURL+"/comment", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("comment: %d", resp.StatusCode)
	}

	// Alice closes it.
	form = url.Values{}
	form.Set("action", "close")
	form.Set("csrf", csrfA)
	resp = postWeb(t, clientA, base, issueURL+"/state", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("close issue: %d", resp.StatusCode)
	}
	page = getWeb(t, clientA, base, issueURL)
	if !strings.Contains(page, "closed") || !strings.Contains(page, "I can reproduce this") {
		t.Fatal("issue not closed or comment missing")
	}

	// ---- 2. Create a PR.
	newPullURL := "/" + alice + "/collab/pulls/new"
	form = url.Values{}
	form.Set("title", "Add feature file")
	form.Set("body", "This adds the feature.")
	form.Set("base", "main")
	form.Set("head", "feature")
	csrfA = getCSRF(t, clientA, base, newPullURL)
	form.Set("csrf", csrfA)
	resp = postWeb(t, clientA, base, newPullURL, form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("PR create: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	pullURL := resp.Header.Get("Location")
	if !strings.Contains(pullURL, "/pulls/1") {
		t.Fatalf("PR redirect = %q", pullURL)
	}

	// PR detail shows commits and diff.
	page = getWeb(t, clientA, base, pullURL)
	if !strings.Contains(page, "add feature") {
		t.Fatal("PR page missing commit")
	}
	if !strings.Contains(page, "feature.txt") {
		t.Fatal("PR page missing diff content")
	}
	if !strings.Contains(page, "Add feature file") {
		t.Fatal("PR title missing")
	}

	// ---- 3. Branch protection: require approval.
	_, _ = s.store.AddBranchRule(ctx, store.BranchRule{
		RepositoryID:    repo.ID,
		Pattern:         "main",
		RequireApproval: true,
	})

	// Merge without approval → blocked.
	form = url.Values{}
	form.Set("strategy", "merge-commit")
	form.Set("csrf", getCSRF(t, clientA, base, pullURL))
	resp = postWeb(t, clientA, base, pullURL+"/merge", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("merge attempt: %d", resp.StatusCode)
	}
	// Follow the redirect and check for the error flash.
	page = getWeb(t, clientA, base, pullURL+"?msg=err-merge")
	if !strings.Contains(page, "open") || strings.Contains(page, "merged") {
		t.Fatalf("PR merged without approval (state on page: still should be open)")
	}

	// Bob approves.
	form = url.Values{}
	form.Set("verdict", "approve")
	form.Set("body", "lgtm")
	form.Set("csrf", getCSRF(t, clientB, base, pullURL))
	resp = postWeb(t, clientB, base, pullURL+"/review", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("review: %d", resp.StatusCode)
	}

	// Now alice merges.
	form = url.Values{}
	form.Set("strategy", "merge-commit")
	form.Set("csrf", getCSRF(t, clientA, base, pullURL))
	resp = postWeb(t, clientA, base, pullURL+"/merge", form)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("merge: %d", resp.StatusCode)
	}

	// PR is merged and main has the feature.
	page = getWeb(t, clientA, base, pullURL)
	if !strings.Contains(page, "merged") {
		t.Fatalf("PR not merged after merge:\n%s", page[:min(len(page), 500)])
	}

	// Verify on the server side.
	mustGit(t, bare, "log", "--format=%s", "refs/heads/main")
	out := mustGit(t, bare, "log", "--format=%s", "refs/heads/main")
	if !strings.Contains(out, "Merge pull request #1") {
		t.Fatalf("main lacks merge commit:\n%s", out)
	}
	// The feature file is present.
	if out2 := mustGit(t, bare, "ls-tree", "refs/heads/main"); !strings.Contains(out2, "feature.txt") {
		t.Fatalf("main lacks feature.txt:\n%s", out2)
	}
	// fsck still clean.
	if out3 := mustGit(t, bare, "fsck", "--strict"); strings.Contains(out3, "error") {
		t.Fatalf("fsck after merge:\n%s", out3)
	}

	_ = clientB
}

func runWork(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec2(t, dir, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func loginWeb(t *testing.T, client *http.Client, base, username, password string) {
	t.Helper()
	// Get the login page (sets the visitor cookie for CSRF binding).
	resp, err := client.Get(base + "/login")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 65536)
	n, _ := resp.Body.Read(buf)
	resp.Body.Close()
	page := string(buf[:n])

	csrf := extractCSRF(t, page)
	form := url.Values{}
	form.Set("csrf", csrf)
	form.Set("username", username)
	form.Set("password", password)

	postResp, err := client.PostForm(base+"/login", form)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, postResp.Body)
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("login %s: %d (want 303)", username, postResp.StatusCode)
	}
}

func getCSRF(t *testing.T, client *http.Client, base, path string) string {
	t.Helper()
	resp, err := client.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 65536)
	n, _ := resp.Body.Read(buf)
	page := string(buf[:n])
	return extractCSRF(t, page)
}

func extractCSRF(t *testing.T, page string) string {
	t.Helper()
	m := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no CSRF token in page (len %d):\n%s", len(page), page[:min(len(page), 300)])
	}
	return m[1]
}

func getWeb(t *testing.T, client *http.Client, base, path string) string {
	t.Helper()
	resp, err := client.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

type webResponse struct {
	*http.Response
	Body string
}

func postWeb(t *testing.T, client *http.Client, base, path string, form url.Values) *webResponse {
	t.Helper()
	resp, err := client.PostForm(base+path, form)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return &webResponse{Response: resp, Body: string(body)}
}

func exec2(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=W", "GIT_AUTHOR_EMAIL=w@t.test",
		"GIT_COMMITTER_NAME=W", "GIT_COMMITTER_EMAIL=w@t.test",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	return cmd
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = time.Second
var _ = strconv.Itoa
