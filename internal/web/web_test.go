package web

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/csrf"
	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/ratelimit"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/security"
	"githost.dev/githost/internal/session"
	"githost.dev/githost/internal/storage"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/migrations"
)

// harness is a fully wired web UI backed by real SQLite and a real git
// executor, served over httptest.
type harness struct {
	t       *testing.T
	server  *httptest.Server
	client  *http.Client
	st      *store.Store
	svc     *reposervice.Service
	gitx    *git.Executor
	repoDir string
	users   map[string]*store.User
}

var harnessUserSeq atomic.Int64

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()

	db, err := database.Open(ctx, filepath.Join(base, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)

	executor, err := git.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(base, "repositories")
	repoStorage, err := storage.NewRepositories(repoDir, executor)
	if err != nil {
		t.Fatal(err)
	}
	svc := reposervice.New(st, repoStorage)

	secretFile := filepath.Join(base, "secret.key")
	if err := security.WriteSecretFile(secretFile); err != nil {
		t.Fatal(err)
	}
	secret, err := security.LoadSecretFile(secretFile)
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	_, err = Register(mux, Deps{
		Store:    st,
		Sessions: session.New(st, time.Hour),
		CSRF:     csrf.New(secret),
		Perms:    permissions.New(st),
		Repos:    svc,
		Git:      executor,
		Limiter:  ratelimit.New(5, time.Minute),
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &harness{
		t:      t,
		server: server,
		client: &http.Client{Jar: jar, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // follow manually for assertions
		}},
		st:      st,
		svc:     svc,
		gitx:    executor,
		repoDir: repoDir,
		users:   make(map[string]*store.User),
	}
}

// createUser registers an account with a real password hash.
func (h *harness) createUser(name, password string) *store.User {
	h.t.Helper()
	ctx := context.Background()
	hash, err := auth.HashPassword(password)
	if err != nil {
		h.t.Fatal(err)
	}
	name = store.NormalizeUsername(name)
	user, err := h.st.CreateUser(ctx, name, name+"@example.test", hash, false)
	if err != nil {
		h.t.Fatal(err)
	}
	h.users[name] = user
	return user
}

// login performs the full login flow (visitor cookie + CSRF) using the
// harness client (shared cookie jar).
func (h *harness) login(username, password string) int {
	h.t.Helper()
	page := h.get("/login")
	token := csrfTokenFromHTML(h.t, page)

	form := url.Values{}
	form.Set("csrf", token)
	form.Set("username", username)
	form.Set("password", password)
	resp := h.postForm("/login", form)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func (h *harness) get(path string) string {
	h.t.Helper()
	resp, err := h.client.Get(h.server.URL + path)
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("GET %s: status %d\n%s", path, resp.StatusCode, body)
	}
	return string(body)
}

// resetSession swaps the cookie jar for a fresh login identity.
func (h *harness) resetSession() {
	h.t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.client.Jar = jar
}

func (h *harness) getRaw(path string) *http.Response {
	h.t.Helper()
	resp, err := h.client.Get(h.server.URL + path)
	if err != nil {
		h.t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func (h *harness) postForm(path string, form url.Values) *http.Response {
	h.t.Helper()
	resp, err := h.client.PostForm(h.server.URL+path, form)
	if err != nil {
		h.t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

var csrfPattern = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func csrfTokenFromHTML(t *testing.T, body string) string {
	t.Helper()
	m := csrfPattern.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no csrf token in page:\n%s", body)
	}
	return m[1]
}

// seedRepoContent pushes real commits into a repository created through
// the service (the "pusher" here is a test-side git client).
func (h *harness) seedRepoContent(owner, name string) {
	h.t.Helper()
	repo, err := h.svc.Resolve(context.Background(), owner, name)
	if err != nil {
		h.t.Fatal(err)
	}
	barePath := filepath.Join(h.repoDir, repo.UUID+".git")

	work := filepath.Join(h.t.TempDir(), "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		h.t.Fatal(err)
	}
	run := func(args ...string) {
		h.t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Web", "GIT_AUTHOR_EMAIL=web@example.test",
			"GIT_COMMITTER_NAME=Web", "GIT_COMMITTER_EMAIL=web@example.test")
		if out, err := cmd.CombinedOutput(); err != nil {
			h.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--initial-branch=main", work)
	os.WriteFile(filepath.Join(work, "README.md"), []byte("# seeded project\nweb ui test\n"), 0o644)
	os.MkdirAll(filepath.Join(work, "src"), 0o755)
	os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main\n"), 0o644)
	run("add", ".")
	run("commit", "-m", "seeded content")
	run("push", filepath.ToSlash(barePath), "main")
	run("tag", "-a", "v1.0", "-m", "first tag")
	run("push", filepath.ToSlash(barePath), "refs/tags/v1.0")
}

// TestLoginFlowAndCSRF verifies the full session lifecycle.
func TestLoginFlowAndCSRF(t *testing.T) {
	h := newHarness(t)
	h.createUser("alice", "correct-password-1")

	// Anonymous dashboard renders and offers login.
	page := h.get("/")
	if !strings.Contains(page, "Log in") {
		t.Fatal("anonymous page lacks login link")
	}
	if strings.Contains(page, "alice") {
		t.Fatal("anonymous page shows user chrome")
	}

	// Security headers present.
	resp := h.getRaw("/")
	if resp.Header.Get("Content-Security-Policy") == "" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("security headers missing")
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Login without CSRF token is rejected.
	bad := h.postForm("/login", url.Values{
		"username": {"alice"}, "password": {"correct-password-1"},
	})
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("login without csrf: status %d", bad.StatusCode)
	}

	// Wrong password fails.
	if code := h.login("alice", "wrong-password-1"); code != http.StatusUnauthorized {
		t.Fatalf("wrong password login status = %d", code)
	}

	// Correct login redirects to the dashboard.
	if code := h.login("alice", "correct-password-1"); code != http.StatusSeeOther {
		t.Fatalf("correct login status = %d", code)
	}
	page = h.get("/")
	if !strings.Contains(page, "alice") || strings.Contains(page, "Log in</a>") {
		t.Fatalf("logged-in chrome wrong:\n%s", page[:min(len(page), 300)])
	}
}

// TestLoginRateLimit locks online guessing out.
func TestLoginRateLimit(t *testing.T) {
	h := newHarness(t)
	h.createUser("bob", "correct-password-2")

	for i := 0; i < 5; i++ {
		if code := h.login("bob", "nope-"+string(rune('a'+i))); code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i, code)
		}
	}
	if code := h.login("bob", "correct-password-2"); code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited login status = %d, want 429", code)
	}
}

// TestRepoCreateAndView walks the repository creation form and the pages
// that render repository data.
func TestRepoCreateAndView(t *testing.T) {
	h := newHarness(t)
	h.createUser("alice", "correct-password-1")
	if code := h.login("alice", "correct-password-1"); code != http.StatusSeeOther {
		t.Fatalf("login failed: %d", code)
	}

	// Anonymous repo creation redirects to login (fresh client).
	freshJar, _ := cookiejar.New(nil)
	anon := &http.Client{Jar: freshJar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	anonResp, err := anon.Get(h.server.URL + "/new")
	if err != nil {
		t.Fatal(err)
	}
	anonResp.Body.Close()
	if anonResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("anonymous /new status = %d", anonResp.StatusCode)
	}

	// Create via form (with CSRF).
	formPage := h.get("/new")
	form := url.Values{}
	form.Set("csrf", csrfTokenFromHTML(t, formPage))
	form.Set("name", "my-project")
	form.Set("description", "created from the web ui")
	form.Set("visibility", "private")
	form.Set("default_branch", "main")
	resp := h.postForm("/new", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		h.t.Fatalf("repo create status = %d", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location != "/alice/my-project?msg=repo-created" {
		t.Fatalf("create redirect = %q", location)
	}

	// Duplicate name re-renders with a friendly error.
	formPage = h.get("/new")
	form.Set("csrf", csrfTokenFromHTML(t, formPage))
	dup := h.postForm("/new", form)
	dupBody, _ := io.ReadAll(dup.Body)
	dup.Body.Close()
	if dup.StatusCode != http.StatusBadRequest ||
		!strings.Contains(string(dupBody), "already in use") ||
		!strings.Contains(string(dupBody), "my-project") {
		t.Fatalf("duplicate create: status %d body=%s", dup.StatusCode, dupBody)
	}

	// Empty-repository home page shows clone help.
	page := h.get("/alice/my-project")
	if !strings.Contains(page, "Empty repository") || !strings.Contains(page, "git clone") {
		t.Fatalf("empty repo page wrong:\n%s", page[:min(len(page), 400)])
	}

	// Push real content and browse.
	h.seedRepoContent("alice", "my-project")

	page = h.get("/alice/my-project")
	// README.md renders as safe HTML (markdown-body), not raw text.
	if !strings.Contains(page, "markdown-body") || !strings.Contains(page, "seeded project") ||
		strings.Contains(page, "# seeded project") {
		t.Fatalf("README not rendered on repo home:\n%s", page[:min(len(page), 400)])
	}
	if !strings.Contains(page, "seeded content") {
		t.Fatal("latest commit subject missing")
	}

	// Tree and blob.
	page = h.get("/alice/my-project/tree/src")
	if !strings.Contains(page, "main.go") {
		t.Fatal("tree listing missing file")
	}
	blob := h.get("/alice/my-project/blob/src/main.go?ref=main")
	if !strings.Contains(blob, "package main") {
		t.Fatal("blob content missing")
	}

	// Raw route streams the exact bytes.
	raw := h.getRaw("/alice/my-project/raw/README.md?ref=main")
	body, _ := io.ReadAll(raw.Body)
	raw.Body.Close()
	if string(body) != "# seeded project\nweb ui test\n" {
		t.Fatalf("raw content = %q", body)
	}

	// Commits, commit detail, branches, tags.
	page = h.get("/alice/my-project/commits")
	if !strings.Contains(page, "seeded content") {
		t.Fatal("commit list missing seeded commit")
	}
	shaPattern := regexp.MustCompile(`/alice/my-project/commit/([0-9a-f]{40})`)
	m := shaPattern.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no commit link on commits page")
	}
	page = h.get("/alice/my-project/commit/" + m[1])
	if !strings.Contains(page, "new file mode") {
		t.Fatal("commit patch missing")
	}
	page = h.get("/alice/my-project/branches")
	if !strings.Contains(page, "main") || !strings.Contains(page, "default") {
		t.Fatal("branches page wrong")
	}
	page = h.get("/alice/my-project/tags")
	if !strings.Contains(page, "v1.0") || !strings.Contains(page, "first tag") {
		t.Fatal("tags page wrong")
	}
}

// TestPrivateRepoIs404ForOthers checks the web privacy policy.
func TestPrivateRepoIs404ForOthers(t *testing.T) {
	h := newHarness(t)
	alice := h.createUser("alice", "correct-password-1")
	h.createUser("mallory", "correct-password-3")
	h.seedRepoWithOwner(alice, "secret")

	// Anonymous viewer: 404.
	resp := h.getRaw("/alice/secret")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("anonymous private repo: %d", resp.StatusCode)
	}

	// Stranger with a session: 404 (existence hidden).
	if code := h.login("mallory", "correct-password-3"); code != http.StatusSeeOther {
		t.Fatalf("mallory login: %d", code)
	}
	resp = h.getRaw("/alice/secret")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("stranger private repo: %d", resp.StatusCode)
	}

	// Owner sees it.
	h.resetSession()
	if code := h.login("alice", "correct-password-1"); code != http.StatusSeeOther {
		t.Fatalf("alice re-login: %d", code)
	}
	page := h.get("/alice/secret")
	if !strings.Contains(page, "secret") {
		t.Fatal("owner cannot see own repo")
	}
}

// seedRepoWithOwner creates a private repo owned by user.
func (h *harness) seedRepoWithOwner(owner *store.User, name string) {
	h.t.Helper()
	_, err := h.svc.Create(context.Background(), owner, name, reposervice.CreateOptions{})
	if err != nil {
		h.t.Fatal(err)
	}
}

// TestSettingsPagesAndTokenReveal walks keys and tokens pages.
func TestSettingsPagesAndTokenReveal(t *testing.T) {
	h := newHarness(t)
	h.createUser("alice", "correct-password-1")
	if code := h.login("alice", "correct-password-1"); code != http.StatusSeeOther {
		t.Fatalf("login: %d", code)
	}

	// Add an SSH key via the form (a real, generated key).
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	keyPage := h.get("/settings/keys")
	form := url.Values{}
	form.Set("csrf", csrfTokenFromHTML(t, keyPage))
	form.Set("title", "laptop")
	form.Set("key", strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))))
	resp := h.postForm("/settings/keys/add", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("key add status = %d", resp.StatusCode)
	}
	page := h.get("/settings/keys")
	if !strings.Contains(page, "SHA256:") || !strings.Contains(page, "laptop") {
		t.Fatalf("key not listed:\n%s", page)
	}

	// Create a token via the form; plaintext shown exactly once.
	tokenPage := h.get("/settings/tokens")
	form = url.Values{}
	form.Set("csrf", csrfTokenFromHTML(t, tokenPage))
	form.Set("name", "ci")
	form.Set("scope_read", "1")
	form.Set("scope_write", "1")
	resp = h.postForm("/settings/tokens/create", form)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token create status = %d", resp.StatusCode)
	}
	reveal := regexp.MustCompile(`ght_[0-9a-f]+`).FindString(string(body))
	if reveal == "" {
		t.Fatalf("token not revealed:\n%s", body)
	}
	// Reload: the full plaintext is never shown again (the listing shows
	// only the prefix).
	page = h.get("/settings/tokens")
	if strings.Contains(page, reveal) {
		t.Fatal("token plaintext shown twice")
	}
	if !strings.Contains(page, "ci") {
		t.Fatal("token not listed after creation")
	}

	// The created token authenticates Git over HTTPS (cross-check with the
	// store) — verify it resolves and has the right scopes.
	tokens, err := h.st.TokensByUser(context.Background(), h.users["alice"].ID)
	if err != nil || len(tokens) == 0 || tokens[0].Scopes != "repo:read repo:write" {
		t.Fatalf("token in store wrong: %v %v", tokens, err)
	}
}

// --- tiny helpers -----------------------------------------------------------

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
