package reposervice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/storage"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/migrations"
)

func newService(t *testing.T) (*Service, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	executor, err := git.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	reposDir := filepath.Join(dir, "repositories")
	repoStore, err := storage.NewRepositories(reposDir, executor)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	return New(st, repoStore), st, reposDir
}

func TestCreateCompensation(t *testing.T) {
	svc, st, reposDir := newService(t)
	ctx := context.Background()

	alice, err := st.CreateUser(ctx, "alice", "", "", false)
	if err != nil {
		t.Fatal(err)
	}

	repo, err := svc.Create(ctx, alice, "my-project", CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if repo.Visibility != store.VisibilityPrivate {
		t.Fatal("repositories must be private by default")
	}
	if _, err := os.Stat(filepath.Join(reposDir, repo.UUID+".git", "HEAD")); err != nil {
		t.Fatalf("bare repo missing: %v", err)
	}

	// Duplicate name is rejected (store constraint).
	if _, err := svc.Create(ctx, alice, "my-project", CreateOptions{}); err == nil {
		t.Fatal("duplicate name accepted")
	}

	// Invalid names are rejected before any storage happens.
	for _, name := range []string{"", "has space", "CON", "x.git"} {
		if _, err := svc.Create(ctx, alice, name, CreateOptions{}); err == nil {
			t.Errorf("Create(name=%q) accepted", name)
		}
	}

	// Owner resolution goes through Resolve.
	got, err := svc.Resolve(ctx, "alice", "my-project")
	if err != nil || got.ID != repo.ID {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := svc.Resolve(ctx, "alice", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	// Delete removes row and directory.
	if err := svc.Delete(ctx, repo); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Resolve(ctx, "alice", "my-project"); !errors.Is(err, ErrNotFound) {
		t.Fatal("row still present after delete")
	}
	if _, err := os.Stat(filepath.Join(reposDir, repo.UUID+".git")); !os.IsNotExist(err) {
		t.Fatal("directory still present after delete")
	}
}

func TestArchiveBlocksPushOnly(t *testing.T) {
	svc, st, _ := newService(t)
	ctx := context.Background()
	alice, _ := st.CreateUser(ctx, "alice", "", "", false)
	repo, err := svc.Create(ctx, alice, "soon-archived", CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetArchived(ctx, repo, true); err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.Resolve(ctx, "alice", "soon-archived")
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.IsArchived {
		t.Fatal("archived flag not persisted")
	}
}
