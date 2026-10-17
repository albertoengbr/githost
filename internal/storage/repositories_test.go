package storage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"githost.dev/githost/internal/git"
)

func newStore(t *testing.T) *Repositories {
	t.Helper()
	executor, err := git.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	repos, err := NewRepositories(filepath.Join(t.TempDir(), "repositories"), executor)
	if err != nil {
		t.Fatal(err)
	}
	return repos
}

func TestPathValidation(t *testing.T) {
	repos := newStore(t)

	path, err := repos.Path("0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatalf("Path(valid): %v", err)
	}
	if !strings.HasSuffix(path, ".git") {
		t.Fatalf("path = %q, want .git suffix", path)
	}
	if !strings.HasPrefix(path, repos.Root()) {
		t.Fatalf("path %q outside root %q", path, repos.Root())
	}

	for _, bad := range []string{
		"", "abc", "UPPERCASE0123456789ABCDEF01234",
		"0123456789abcdef0123456789abcde", // 31 chars
		"0123456789abcdef0123456789abcdeff/../../escape",
		"..", "sub/dir/name",
	} {
		if _, err := repos.Path(bad); err == nil {
			t.Errorf("Path(%q) accepted", bad)
		}
	}
}

func TestCreateAndDelete(t *testing.T) {
	repos := newStore(t)
	ctx := context.Background()
	uuid := "0123456789abcdef0123456789abcdef"

	path, err := repos.Create(ctx, uuid, "main")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "HEAD")); err != nil {
		t.Fatalf("bare repository not initialized: %v", err)
	}
	head, err := os.ReadFile(filepath.Join(path, "HEAD"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(head)) != "ref: refs/heads/main" {
		t.Fatalf("HEAD = %q, want main default branch", strings.TrimSpace(string(head)))
	}

	// Duplicate creation is refused.
	if _, err := repos.Create(ctx, uuid, "main"); err == nil {
		t.Fatal("duplicate Create accepted")
	}

	// Invalid branch names are rejected before touching the disk.
	if _, err := repos.Create(ctx, "11111111111111111111111111111111", "not a branch"); err == nil {
		t.Fatal("invalid branch accepted")
	}

	if err := repos.Delete(uuid); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("repository directory still present: %v", err)
	}
	// Deleting again is a no-op.
	if err := repos.Delete(uuid); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
}
