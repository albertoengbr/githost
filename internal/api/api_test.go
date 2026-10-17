package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/storage"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/migrations"
)

type harness struct {
	t       *testing.T
	server  *httptest.Server
	st      *store.Store
	svc     *reposervice.Service
	repoDir string
	users   map[string]*store.User
}

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

	mux := http.NewServeMux()
	Register(mux, Deps{
		Store:  st,
		Perms:  permissions.New(st),
		Repos:  svc,
		Git:    executor,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &harness{t: t, server: server, st: st, svc: svc, repoDir: repoDir, users: map[string]*store.User{}}
}

// userWithToken creates an account and returns its plaintext token.
func (h *harness) userWithToken(username, scopes string) (string, string) {
	h.t.Helper()
	ctx := context.Background()
	hash, _ := auth.HashPassword("irrelevant-password-1")
	user, err := h.st.CreateUser(ctx, username, "", hash, false)
	if err != nil {
		h.t.Fatal(err)
	}
	token, tokenHash, _, err := auth.GenerateToken()
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.st.CreateToken(ctx, user.ID, "test", tokenHash, token[:10], scopes, nil); err != nil {
		h.t.Fatal(err)
	}
	h.users[username] = user
	return user.Username, token
}

func (h *harness) createRepo(owner, name string, public bool) {
	h.t.Helper()
	visibility := store.VisibilityPrivate
	if public {
		visibility = store.VisibilityPublic
	}
	if _, err := h.svc.Create(context.Background(), h.users[owner], name, reposervice.CreateOptions{
		Visibility: visibility,
	}); err != nil {
		h.t.Fatal(err)
	}
}

type response struct {
	status int
	body   string
}

func (h *harness) do(method, path, token, body string) response {
	h.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "token "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return response{status: resp.StatusCode, body: string(raw)}
}

// TestAuthAndScopes walks the authentication rules.
func TestAuthAndScopes(t *testing.T) {
	h := newHarness(t)
	alice, aliceToken := h.userWithToken("alice", "repo:read repo:write")
	_, readToken := h.userWithToken("reader", "repo:read")

	// No token → 401.
	if r := h.do("GET", "/api/v1/user", "", ""); r.status != http.StatusUnauthorized {
		t.Fatalf("no token: %d", r.status)
	}
	// Bad token → 401.
	if r := h.do("GET", "/api/v1/user", "ght_notreal", ""); r.status != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", r.status)
	}
	// Bearer form works.
	req, _ := http.NewRequest("GET", h.server.URL+"/api/v1/user", nil)
	req.Header.Set("Authorization", "Bearer "+aliceToken)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bearer: %d", resp.StatusCode)
	}

	// Current user payload.
	r := h.do("GET", "/api/v1/user", aliceToken, "")
	if r.status != 200 || !strings.Contains(r.body, `"username":"alice"`) {
		t.Fatalf("current user: %d %s", r.status, r.body)
	}

	// Write scope needed for creation.
	if r := h.do("POST", "/api/v1/repos", readToken,
		`{"name":"nope"}`); r.status != http.StatusForbidden {
		t.Fatalf("read-only create: %d", r.status)
	}
	_ = alice
}

// TestRepoLifecycleAndPrivacy exercises create/read/update/delete plus the
// 404-hides-private policy.
func TestRepoLifecycleAndPrivacy(t *testing.T) {
	h := newHarness(t)
	alice, aliceToken := h.userWithToken("alice", "repo:read repo:write")
	mallory, malloryToken := h.userWithToken("mallory", "repo:read repo:write")
	_, readToken := h.userWithToken("reader", "repo:read")

	// Create.
	r := h.do("POST", "/api/v1/repos", aliceToken,
		`{"name":"proj","description":"api made","visibility":"private"}`)
	if r.status != http.StatusCreated || !strings.Contains(r.body, `"name":"proj"`) {
		t.Fatalf("create: %d %s", r.status, r.body)
	}
	// Invalid name.
	if r := h.do("POST", "/api/v1/repos", aliceToken, `{"name":"CON"}`); r.status != http.StatusBadRequest {
		t.Fatalf("create CON: %d", r.status)
	}
	// Duplicate.
	if r := h.do("POST", "/api/v1/repos", aliceToken, `{"name":"proj"}`); r.status != http.StatusBadRequest ||
		!strings.Contains(r.body, "already in use") {
		t.Fatalf("duplicate: %d %s", r.status, r.body)
	}

	// Owner reads; stranger gets 404 (private).
	if r := h.do("GET", "/api/v1/repos/"+alice+"/proj", aliceToken, ""); r.status != 200 {
		t.Fatalf("owner get: %d", r.status)
	}
	if r := h.do("GET", "/api/v1/repos/"+alice+"/proj", malloryToken, ""); r.status != http.StatusNotFound {
		t.Fatalf("stranger get: %d", r.status)
	}
	if r := h.do("GET", "/api/v1/repos/"+alice+"/proj", readToken, ""); r.status != http.StatusNotFound {
		t.Fatalf("reader get: %d", r.status)
	}

	// Update (make public).
	r = h.do("PATCH", "/api/v1/repos/"+alice+"/proj", aliceToken, `{"visibility":"public"}`)
	if r.status != 200 || !strings.Contains(r.body, `"visibility":"public"`) {
		t.Fatalf("patch: %d %s", r.status, r.body)
	}
	// Now the read-only token can see it.
	if r := h.do("GET", "/api/v1/repos/"+alice+"/proj", readToken, ""); r.status != 200 {
		t.Fatalf("public get after patch: %d", r.status)
	}

	// Stranger cannot update/delete. The repository is public at this
	// point, so its existence is not hidden — the answer is 403.
	if r := h.do("PATCH", "/api/v1/repos/"+alice+"/proj", malloryToken, `{"archived":true}`); r.status != http.StatusForbidden {
		t.Fatalf("stranger patch: %d", r.status)
	}
	if r := h.do("DELETE", "/api/v1/repos/"+alice+"/proj", malloryToken, ""); r.status != http.StatusForbidden {
		t.Fatalf("stranger delete: %d", r.status)
	}
	// On a PRIVATE repository the same stranger gets 404 (existence hidden).
	h.createRepo(alice, "secret", false)
	if r := h.do("PATCH", "/api/v1/repos/"+alice+"/secret", malloryToken, `{"archived":true}`); r.status != http.StatusNotFound {
		t.Fatalf("stranger patch private: %d", r.status)
	}
	_ = mallory

	// Delete by owner.
	if r := h.do("DELETE", "/api/v1/repos/"+alice+"/proj", aliceToken, ""); r.status != http.StatusNoContent {
		t.Fatalf("delete: %d", r.status)
	}
	if r := h.do("GET", "/api/v1/repos/"+alice+"/proj", aliceToken, ""); r.status != http.StatusNotFound {
		t.Fatalf("get after delete: %d", r.status)
	}
}

// TestListingsAndGitData verifies listing endpoints and git-derived data.
func TestListingsAndGitData(t *testing.T) {
	h := newHarness(t)
	alice, aliceToken := h.userWithToken("alice", "repo:read repo:write")
	h.createRepo(alice, "proj", false)
	h.createRepo(alice, "open", true)
	repo, err := h.svc.Resolve(context.Background(), alice, "proj")
	if err != nil {
		t.Fatal(err)
	}

	// Seed content (test-side pusher).
	work := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := execGit(t, work, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	execGit(t, work, "init", "--initial-branch=main", work).Run()
	os.WriteFile(filepath.Join(work, "f.txt"), []byte("hello\n"), 0o644)
	run("add", ".")
	run("commit", "-m", "api seed")
	run("push", filepath.ToSlash(filepath.Join(h.repoDir, repo.UUID+".git")), "main")
	run("tag", "v1.0")
	run("push", filepath.ToSlash(filepath.Join(h.repoDir, repo.UUID+".git")), "refs/tags/v1.0")

	// Own listings.
	r := h.do("GET", "/api/v1/user/repos", aliceToken, "")
	if r.status != 200 ||
		!strings.Contains(r.body, `"name":"proj"`) ||
		!strings.Contains(r.body, `"name":"open"`) {
		t.Fatalf("user repos: %d %s", r.status, r.body)
	}
	r = h.do("GET", "/api/v1/repos", aliceToken, "")
	if r.status != 200 || !strings.Contains(r.body, `"per_page":30`) {
		t.Fatalf("repos list: %d %s", r.status, r.body)
	}

	// Branches, tags, commits.
	r = h.do("GET", "/api/v1/repos/"+alice+"/proj/branches", aliceToken, "")
	if r.status != 200 || !strings.Contains(r.body, `"name":"main"`) {
		t.Fatalf("branches: %d %s", r.status, r.body)
	}
	r = h.do("GET", "/api/v1/repos/"+alice+"/proj/tags", aliceToken, "")
	if r.status != 200 || !strings.Contains(r.body, `"name":"v1.0"`) {
		t.Fatalf("tags: %d %s", r.status, r.body)
	}
	r = h.do("GET", "/api/v1/repos/"+alice+"/proj/commits", aliceToken, "")
	if r.status != 200 || !strings.Contains(r.body, `"subject":"api seed"`) {
		t.Fatalf("commits: %d %s", r.status, r.body)
	}
	// Pagination caps.
	if r := h.do("GET", "/api/v1/repos/"+alice+"/proj/commits?per_page=500", aliceToken, ""); r.status != 200 {
		t.Fatalf("commits big page: %d", r.status)
	}
}

// TestOpenAPIContractSync keeps docs/api/openapi.yaml in sync with the
// registered routes: every route must be documented, and every documented
// path must exist in the implementation.
func TestOpenAPIContractSync(t *testing.T) {
	h := newHarness(t)

	apiRoutes := h.routes(t)

	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "api", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var spec struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}

	// api pattern "/api/v1/repos/{owner}/{repo}" ↔ spec path "/repos/{owner}/{repo}"
	patternToSpec := func(pattern string) string {
		return strings.TrimPrefix(pattern, "/api/v1")
	}

	implemented := make(map[string]bool)
	for _, route := range apiRoutes {
		specPath := patternToSpec(route.Pattern)
		methods, ok := spec.Paths[specPath]
		if !ok {
			t.Errorf("route %s %s missing from docs/api/openapi.yaml (expected path %s)",
				route.Method, route.Pattern, specPath)
			continue
		}
		lower := strings.ToLower(route.Method)
		if _, ok := methods[lower]; !ok {
			t.Errorf("route %s %s missing from openapi.yaml path %s", route.Method, route.Pattern, specPath)
		}
		implemented[specPath+"\x00"+lower] = true
	}
	for path, methods := range spec.Paths {
		for method := range methods {
			if method == "parameters" {
				continue
			}
			if !implemented[path+"\x00"+method] {
				t.Errorf("openapi.yaml documents %s %s but no such route is registered", method, path)
			}
		}
	}
}

// routes extracts the route table by registering on a fresh mux and
// consulting the API's Routes().
func (h *harness) routes(t *testing.T) []Route {
	t.Helper()
	mux := http.NewServeMux()
	a := Register(mux, Deps{Store: h.st, Perms: permissions.New(h.st), Repos: h.svc, Logger: nil})
	return a.Routes()
}

func execGit(t *testing.T, dir string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Api", "GIT_AUTHOR_EMAIL=api@example.test",
		"GIT_COMMITTER_NAME=Api", "GIT_COMMITTER_EMAIL=api@example.test")
	return cmd
}
