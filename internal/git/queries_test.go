package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildFixtureRepo creates a bare repository populated with real content.
// The seeding uses os/exec as the "pusher" (the client side); the tests
// below then exercise the server-side query layer through the executor.
//
// Contents: 2 commits on main (README.md + src/ with two files, one with
// a space in the name), branch "dev" one commit ahead of main's first
// commit, annotated tag "v1.0".
func buildFixtureRepo(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	work := filepath.Join(base, "work")
	bare := filepath.Join(base, "repo.git")

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Fix",
			"GIT_AUTHOR_EMAIL=fix@example.test",
			"GIT_COMMITTER_NAME=Fix",
			"GIT_COMMITTER_EMAIL=fix@example.test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run(base, "init", "--bare", "--initial-branch=main", bare)
	run(base, "init", "--initial-branch=main", work)

	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(work, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("README.md", "# fixture\nline two\n")
	run(work, "add", ".")
	run(work, "commit", "-m", "first commit")

	write("src/main.go", "package main\n\nfunc main() {}\n")
	write("src/notes file.txt", "notes with space\n")
	run(work, "add", ".")
	run(work, "commit", "-m", "second commit")

	run(work, "tag", "-a", "v1.0", "-m", "release one")
	run(work, "branch", "dev")

	run(work, "push", filepath.ToSlash(bare), "main", "dev", "refs/tags/v1.0")
	return bare
}

func TestListBranchesAndTags(t *testing.T) {
	dir := buildFixtureRepo(t)
	e := newExecutor(t)
	ctx := context.Background()

	branches, err := e.ListBranches(ctx, dir)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	names := map[string]bool{}
	for _, b := range branches {
		names[b.Short] = true
		if !strings.HasPrefix(b.Full, "refs/heads/") {
			t.Errorf("branch full name = %q", b.Full)
		}
		if b.When.IsZero() {
			t.Errorf("branch %s: zero timestamp", b.Short)
		}
	}
	if !names["main"] || !names["dev"] {
		t.Fatalf("branches = %v", names)
	}

	tags, err := e.ListTags(ctx, dir)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	if len(tags) != 1 || tags[0].Short != "v1.0" {
		t.Fatalf("tags = %+v", tags)
	}
	if tags[0].Subject != "release one" {
		t.Fatalf("tag subject = %q", tags[0].Subject)
	}
}

func TestLogPaginationAndContent(t *testing.T) {
	dir := buildFixtureRepo(t)
	e := newExecutor(t)
	ctx := context.Background()

	commits, err := e.Log(ctx, dir, "refs/heads/main", 0, 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("commits = %d, want 2", len(commits))
	}
	if commits[0].Subject != "second commit" || commits[1].Subject != "first commit" {
		t.Fatalf("order wrong: %+v %+v", commits[0], commits[1])
	}
	if commits[1].Parents != nil || len(commits[0].Parents) != 1 {
		t.Fatalf("parents wrong")
	}
	if commits[0].AuthorEmail != "fix@example.test" {
		t.Fatalf("author email = %q", commits[0].AuthorEmail)
	}

	// Pagination: skip 1 leaves the first commit.
	page, err := e.Log(ctx, dir, "refs/heads/main", 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Subject != "first commit" {
		t.Fatalf("paged log = %+v", page)
	}

	// Single commit by hash.
	single, err := e.Commit(ctx, dir, commits[0].Hash)
	if err != nil || single.Hash != commits[0].Hash {
		t.Fatalf("Commit by hash: %v", err)
	}

	// Invalid revisions are rejected without spawning git.
	if _, err := e.Log(ctx, dir, "--exec=evil", 0, 10); err == nil {
		t.Fatal("option-shaped revision accepted")
	}
	if _, err := e.Commit(ctx, dir, "short"); err == nil {
		t.Fatal("short hash accepted (ambiguous by design)")
	}
}

func TestCommitPatch(t *testing.T) {
	dir := buildFixtureRepo(t)
	e := newExecutor(t)
	ctx := context.Background()

	commits, _ := e.Log(ctx, dir, "refs/heads/main", 0, 5)

	rootPatch, err := e.CommitPatch(ctx, dir, commits[1].Hash)
	if err != nil {
		t.Fatalf("CommitPatch(root): %v", err)
	}
	if !strings.Contains(rootPatch, "new file mode") || !strings.Contains(rootPatch, "README.md") {
		t.Fatalf("root patch missing file creation:\n%s", rootPatch)
	}

	second, err := e.CommitPatch(ctx, dir, commits[0].Hash)
	if err != nil {
		t.Fatalf("CommitPatch(second): %v", err)
	}
	if !strings.Contains(second, "src/main.go") || !strings.Contains(second, "notes file.txt") {
		t.Fatalf("second patch missing files:\n%s", second)
	}
	if strings.Contains(second, "README.md") {
		t.Fatalf("second patch must not touch README:\n%s", second)
	}
}

func TestLsTreeAndCatFile(t *testing.T) {
	dir := buildFixtureRepo(t)
	e := newExecutor(t)
	ctx := context.Background()

	root, err := e.LsTree(ctx, dir, "refs/heads/main", "")
	if err != nil {
		t.Fatalf("LsTree(root): %v", err)
	}
	rootNames := map[string]TreeEntry{}
	for _, entry := range root {
		rootNames[entry.Name] = entry
	}
	if _, ok := rootNames["README.md"]; !ok {
		t.Fatalf("README.md missing from root listing: %+v", root)
	}
	if entry, ok := rootNames["src"]; !ok || entry.Type != "tree" || entry.Size != -1 {
		t.Fatalf("src tree entry wrong: %+v", entry)
	}

	src, err := e.LsTree(ctx, dir, "refs/heads/main", "src")
	if err != nil {
		t.Fatalf("LsTree(src): %v", err)
	}
	if len(src) != 2 {
		t.Fatalf("src entries = %d, want 2", len(src))
	}
	var notes *TreeEntry
	for i := range src {
		if src[i].Name == "notes file.txt" {
			notes = &src[i]
		}
	}
	if notes == nil || notes.Size != int64(len("notes with space\n")) {
		t.Fatalf("notes entry wrong: %+v", notes)
	}

	content, err := e.CatFileBlob(ctx, dir, "refs/heads/main", "README.md", 1<<20)
	if err != nil {
		t.Fatalf("CatFileBlob: %v", err)
	}
	if string(content) != "# fixture\nline two\n" {
		t.Fatalf("README content = %q", content)
	}

	// Oversized blob refused.
	if _, err := e.CatFileBlob(ctx, dir, "refs/heads/main", "README.md", 4); err == nil {
		t.Fatal("oversized blob accepted")
	} else if _, ok := err.(*ErrBlobTooLarge); !ok {
		t.Fatalf("err = %v, want ErrBlobTooLarge", err)
	}

	// Binary detection.
	if !IsBinary([]byte{0x61, 0x00, 0x62}) {
		t.Fatal("NUL byte not detected as binary")
	}
	if IsBinary([]byte("plain text\n")) {
		t.Fatal("text detected as binary")
	}

	// Path traversal is rejected before spawning git.
	if _, err := e.LsTree(ctx, dir, "refs/heads/main", "../escape"); err == nil {
		t.Fatal("traversal path accepted")
	}
	if _, err := e.CatFileBlob(ctx, dir, "refs/heads/main", "a/../README.md", 100); err == nil {
		t.Fatal("dot segment accepted")
	}
}

func TestDefaultBranch(t *testing.T) {
	dir := buildFixtureRepo(t)
	e := newExecutor(t)
	ctx := context.Background()

	branch, err := e.DefaultBranch(ctx, dir)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if branch != "main" {
		t.Fatalf("default branch = %q", branch)
	}

	if err := e.SetDefaultBranch(ctx, dir, "dev"); err != nil {
		t.Fatalf("SetDefaultBranch: %v", err)
	}
	branch, _ = e.DefaultBranch(ctx, dir)
	if branch != "dev" {
		t.Fatalf("default branch after set = %q", branch)
	}

	if err := e.SetDefaultBranch(ctx, dir, "-H flag"); err == nil {
		t.Fatal("invalid branch accepted")
	}
}
