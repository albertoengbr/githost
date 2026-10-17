package store

import (
	"context"
	"path/filepath"
	"testing"

	"githost.dev/githost/internal/database"
	"githost.dev/githost/migrations"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := database.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return New(db)
}

func TestUserCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, "alice", "alice@example.test", "some-hash", false)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == 0 || u.Username != "alice" || u.IsAdmin {
		t.Fatalf("unexpected user: %+v", u)
	}

	got, err := s.UserByUsername(ctx, "ALICE") // lookup normalizes case
	if err != nil || got.ID != u.ID {
		t.Fatalf("UserByUsername: %v %+v", err, got)
	}
	got, err = s.UserByID(ctx, u.ID)
	if err != nil || got.Username != "alice" {
		t.Fatalf("UserByID: %v", err)
	}

	if _, err := s.CreateUser(ctx, "alice", "dupe@example.test", "", false); err == nil {
		t.Fatal("duplicate username must fail")
	}
	if _, err := s.UserByUsername(ctx, "bob"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if n, err := s.CountUsers(ctx); err != nil || n != 1 {
		t.Fatalf("CountUsers = %d, %v", n, err)
	}
}

func TestRepositoryLookupAndMembers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	alice, err := s.CreateUser(ctx, "alice", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser(ctx, "bob", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	uuid, err := NewUUID()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.CreateRepository(ctx, alice.ID, uuid, "my-project", "desc", VisibilityPrivate, "main")
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	if repo.Owner != "alice" || repo.Visibility != VisibilityPrivate || repo.IsArchived {
		t.Fatalf("unexpected repo: %+v", repo)
	}

	got, err := s.RepositoryByOwnerAndName(ctx, "alice", "my-project")
	if err != nil || got.ID != repo.ID || got.UUID != uuid {
		t.Fatalf("RepositoryByOwnerAndName: %v %+v", err, got)
	}
	if _, err := s.RepositoryByOwnerAndName(ctx, "alice", "nope"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	// Owner name mismatch must not leak repositories.
	if _, err := s.RepositoryByOwnerAndName(ctx, "bob", "my-project"); err != ErrNotFound {
		t.Fatal("cross-owner lookup must not find the repo")
	}

	// Duplicate (owner, name) is rejected.
	uuid2, _ := NewUUID()
	if _, err := s.CreateRepository(ctx, alice.ID, uuid2, "my-project", "", VisibilityPrivate, "main"); err == nil {
		t.Fatal("duplicate repository name must fail")
	}

	// Members.
	if err := s.AddMember(ctx, repo.ID, bob.ID, PermissionWrite); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	perm, ok, err := s.MemberPermission(ctx, repo.ID, bob.ID)
	if err != nil || !ok || perm != PermissionWrite {
		t.Fatalf("MemberPermission: %q %v %v", perm, ok, err)
	}
	perm, ok, err = s.MemberPermission(ctx, repo.ID, alice.ID)
	if err != nil || ok {
		t.Fatalf("owner should not be a row member (special-cased): %v", err)
	}
	if err := s.RemoveMember(ctx, repo.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.MemberPermission(ctx, repo.ID, bob.ID); ok {
		t.Fatal("membership must be gone after removal")
	}

	// Update + delete.
	if err := s.UpdateRepository(ctx, repo.ID,
		nil, strPtr("new desc"), strPtr(VisibilityPublic), nil, nil); err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}
	got, _ = s.RepositoryByID(ctx, repo.ID)
	if got.Description != "new desc" || got.Visibility != VisibilityPublic {
		t.Fatalf("update not applied: %+v", got)
	}
	if err := s.DeleteRepository(ctx, repo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RepositoryByID(ctx, repo.ID); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestTokensAndSSHKeysAndAudit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	alice, err := s.CreateUser(ctx, "alice", "", "", false)
	if err != nil {
		t.Fatal(err)
	}

	tokenHash := HashToken("ght_deadbeef")
	tok, err := s.CreateToken(ctx, alice.ID, "ci", tokenHash, "ght_dead", "repo:read repo:write", nil)
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if !tok.Active() {
		t.Fatal("fresh token must be active")
	}
	got, err := s.TokenByHash(ctx, tokenHash)
	if err != nil || got.ID != tok.ID || got.Scopes != "repo:read repo:write" {
		t.Fatalf("TokenByHash: %v %+v", err, got)
	}
	if _, err := s.TokenByHash(ctx, HashToken("unknown")); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := s.RevokeToken(ctx, tok.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.TokenByHash(ctx, tokenHash)
	if got.Active() {
		t.Fatal("revoked token must be inactive")
	}
	// Revoking someone else's token is a no-op.
	if err := s.RevokeToken(ctx, tok.ID, alice.ID+999); err != nil {
		t.Fatalf("RevokeToken(other) err = %v", err)
	}

	key, err := s.AddSSHKey(ctx, alice.ID, "SHA256:abc", "ssh-ed25519 AAAA", "laptop")
	if err != nil {
		t.Fatalf("AddSSHKey: %v", err)
	}
	if _, err := s.SSHKeyByFingerprint(ctx, "SHA256:abc"); err != nil {
		t.Fatalf("SSHKeyByFingerprint: %v", err)
	}
	if _, err := s.SSHKeyByFingerprint(ctx, "SHA256:zzz"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	keys, err := s.SSHKeysByUser(ctx, alice.ID)
	if err != nil || len(keys) != 1 || keys[0].ID != key.ID {
		t.Fatalf("SSHKeysByUser: %v %v", keys, err)
	}

	if err := s.Audit(ctx, &alice.ID, "test.action", "alice/my-project", "detail"); err != nil {
		t.Fatalf("Audit: %v", err)
	}
	var n int
	if err := s.DB().QueryRowContext(ctx, "SELECT count(*) FROM audit_events").Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d, err %v", n, err)
	}
}

func TestValidationHelpers(t *testing.T) {
	for _, good := range []string{"a", "ab", "alice", "alice-dev", "a.b", "user_1", strings39()} {
		if err := ValidateUsername(good); err != nil {
			t.Errorf("ValidateUsername(%q) = %v", good, err)
		}
	}
	for _, bad := range []string{
		"", "-alice", "alice-", ".alice", "alice.", "Alice", "al ice", "a..b",
		"way" + strings39() + "too-long-name-exceeds-thirty-nine-characters-limit",
	} {
		if err := ValidateUsername(bad); err == nil {
			t.Errorf("ValidateUsername(%q) accepted", bad)
		}
	}

	for _, good := range []string{"a", "my-project", "Project.1", "v2.0-beta_2"} {
		if err := ValidateRepositoryName(good); err != nil {
			t.Errorf("ValidateRepositoryName(%q) = %v", good, err)
		}
	}
	for _, bad := range []string{
		"", ".proj", "proj.", "proj..git", "my project", "a..b", "myproject.git", "CON", "aux.git",
		strings100x(),
	} {
		if err := ValidateRepositoryName(bad); err == nil {
			t.Errorf("ValidateRepositoryName(%q) accepted", bad)
		}
	}

	if err := ValidateVisibility(VisibilityPrivate); err != nil {
		t.Error(err)
	}
	if err := ValidateVisibility("friends"); err == nil {
		t.Error("invalid visibility accepted")
	}
	if err := ValidateMemberPermission(PermissionAdmin); err != nil {
		t.Error(err)
	}
	if err := ValidateMemberPermission("root"); err == nil {
		t.Error("invalid permission accepted")
	}
}

func strPtr(s string) *string { return &s }

func strings39() string {
	b := make([]byte, 39)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func strings100x() string {
	b := make([]byte, 101)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}
