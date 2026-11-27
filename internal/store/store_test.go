package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/security"
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

func TestPruneAuditAndWebhookDeliveries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	cutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	db := s.DB()
	for _, created := range []string{"2000-01-01T00:00:00Z", "2030-01-01T00:00:00Z"} {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO audit_events (actor_id, action, target, detail, created_at) VALUES (NULL, 'test', '', '', ?)", created); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneAuditEvents(ctx, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("PruneAuditEvents = %d, %v; want 1", n, err)
	}

	alice, err := s.CreateUser(ctx, "alice", "", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.CreateRepository(ctx, alice.ID, "0123456789abcdef0123456789abcdef", "proj", "", VisibilityPrivate, "main")
	if err != nil {
		t.Fatal(err)
	}
	hook, err := s.CreateWebhook(ctx, repo.ID, "https://ci.example.test/hook", "long-enough-secret", "push")
	if err != nil {
		t.Fatal(err)
	}
	for _, created := range []string{"2000-01-01T00:00:00Z", "2030-01-01T00:00:00Z"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO webhook_deliveries (webhook_id, event, payload, status_code, success, created_at)
			 VALUES (?, 'push', '{}', 200, 1, ?)`, hook.ID, created); err != nil {
			t.Fatal(err)
		}
	}
	n, err = s.PruneWebhookDeliveries(ctx, cutoff)
	if err != nil || n != 1 {
		t.Fatalf("PruneWebhookDeliveries = %d, %v; want 1", n, err)
	}
	remaining, err := s.RecentDeliveries(ctx, hook.ID, 10)
	if err != nil || len(remaining) != 1 {
		t.Fatalf("remaining deliveries = %v, %v; want 1", remaining, err)
	}
	if remaining[0].CreatedAt.Before(cutoff) {
		t.Fatal("old delivery survived pruning")
	}
}

func TestClaimTOTPStep(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "alice", "", "hash", false)
	if err != nil {
		t.Fatal(err)
	}

	ok, err := s.ClaimTOTPStep(ctx, u.ID, 1000)
	if err != nil || !ok {
		t.Fatalf("first claim = %v, %v; want true", ok, err)
	}
	ok, err = s.ClaimTOTPStep(ctx, u.ID, 1000)
	if err != nil || ok {
		t.Fatalf("replayed claim = %v, %v; want false", ok, err)
	}
	ok, err = s.ClaimTOTPStep(ctx, u.ID, 999)
	if err != nil || ok {
		t.Fatalf("older claim = %v, %v; want false", ok, err)
	}
	ok, err = s.ClaimTOTPStep(ctx, u.ID, 1001)
	if err != nil || !ok {
		t.Fatalf("newer claim = %v, %v; want true", ok, err)
	}
	// Re-enabling MFA clears the high water mark.
	if err := s.SetTOTPSecret(ctx, u.ID, "sealed"); err != nil {
		t.Fatal(err)
	}
	ok, err = s.ClaimTOTPStep(ctx, u.ID, 1001)
	if err != nil || !ok {
		t.Fatalf("claim after reset = %v, %v; want true", ok, err)
	}
}

func TestSealWebhookSecrets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	secret := "0123456789abcdef0123456789abcdef"

	alice, err := s.CreateUser(ctx, "alice", "", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.CreateRepository(ctx, alice.ID, "0123456789abcdef0123456789abcdef", "proj", "", VisibilityPrivate, "main")
	if err != nil {
		t.Fatal(err)
	}
	hook, err := s.CreateWebhook(ctx, repo.ID, "https://ci.example.test/hook", "legacy-plaintext-secret", "push")
	if err != nil {
		t.Fatal(err)
	}

	n, err := s.SealWebhookSecrets(ctx, secret)
	if err != nil || n != 1 {
		t.Fatalf("SealWebhookSecrets = %d, %v; want 1", n, err)
	}
	// Idempotent: the second pass must not reseal an already-sealed value.
	n, err = s.SealWebhookSecrets(ctx, secret)
	if err != nil || n != 0 {
		t.Fatalf("second SealWebhookSecrets = %d, %v; want 0", n, err)
	}

	hooks, err := s.WebhooksByRepository(ctx, repo.ID)
	if err != nil || len(hooks) != 1 {
		t.Fatalf("WebhooksByRepository = %v, %v", hooks, err)
	}
	if !security.IsSealed(hooks[0].Secret) || strings.Contains(hooks[0].Secret, "legacy-plaintext") {
		t.Fatalf("stored secret is not sealed: %q", hooks[0].Secret)
	}
	plain, err := security.Open(hooks[0].Secret, secret)
	if err != nil || plain != "legacy-plaintext-secret" {
		t.Fatalf("sealed secret does not open: %q, %v", plain, err)
	}
	_ = hook
}

func TestRevokeAllUserTokens(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	alice, err := s.CreateUser(ctx, "alice", "", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser(ctx, "bob", "", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	for i, user := range []*User{alice, alice, bob} {
		name := fmt.Sprintf("token-%s-%d", user.Username, i)
		if _, err := s.CreateToken(ctx, user.ID, "ci", HashToken(name), "tok_", "repo:read", nil); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.RevokeAllUserTokens(ctx, alice.ID)
	if err != nil || n != 2 {
		t.Fatalf("RevokeAllUserTokens = %d, %v; want 2", n, err)
	}
	// Idempotent: a second call must not touch already-revoked rows.
	n, err = s.RevokeAllUserTokens(ctx, alice.ID)
	if err != nil || n != 0 {
		t.Fatalf("second RevokeAllUserTokens = %d, %v; want 0", n, err)
	}

	aliceTokens, err := s.TokensByUser(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range aliceTokens {
		if tok.Active() {
			t.Fatalf("alice token %d still active after password reset", tok.ID)
		}
	}
	bobTokens, err := s.TokensByUser(ctx, bob.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range bobTokens {
		if !tok.Active() {
			t.Fatalf("bob token %d was revoked by alice's reset", tok.ID)
		}
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
