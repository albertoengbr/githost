package permissions

import (
	"context"
	"path/filepath"
	"testing"

	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/migrations"
)

type fixture struct {
	svc     *Service
	st      *store.Store
	alice   *store.User
	bob     *store.User
	root    *store.User
	private *store.Repository
	public  *store.Repository
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Migrate(context.Background(), db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)

	f := &fixture{st: st, svc: New(st)}
	ctx := context.Background()
	f.alice, _ = st.CreateUser(ctx, "alice", "", "", false)
	f.bob, _ = st.CreateUser(ctx, "bob", "", "", false)
	f.root, _ = st.CreateUser(ctx, "root", "", "", true)
	uuid1, _ := store.NewUUID()
	f.private, _ = st.CreateRepository(ctx, f.alice.ID, uuid1, "secret", "", store.VisibilityPrivate, "main")
	uuid2, _ := store.NewUUID()
	f.public, _ = st.CreateRepository(ctx, f.alice.ID, uuid2, "open", "", store.VisibilityPublic, "main")
	return f
}

func TestForRepository(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	cases := []struct {
		name string
		user *store.User
		repo *store.Repository
		want Level
	}{
		{"anonymous on private", nil, f.private, None},
		{"anonymous on public", nil, f.public, Read},
		{"owner on private", f.alice, f.private, Admin},
		{"owner on public", f.alice, f.public, Admin},
		{"stranger on private", f.bob, f.private, None},
		{"stranger on public", f.bob, f.public, Read},
		{"instance admin on private", f.root, f.private, Admin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := f.svc.ForRepository(ctx, tc.user, tc.repo)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("level = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMemberLevels(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.st.AddMember(ctx, f.private.ID, f.bob.ID, store.PermissionRead); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.ForRepository(ctx, f.bob, f.private); got != Read {
		t.Fatalf("read member = %v", got)
	}
	if err := f.st.RemoveMember(ctx, f.private.ID, f.bob.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.AddMember(ctx, f.private.ID, f.bob.ID, store.PermissionWrite); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.ForRepository(ctx, f.bob, f.private); got != Write {
		t.Fatalf("write member = %v", got)
	}
	// An explicit write grant on a public repository must not be floored
	// to the anonymous Read level.
	if err := f.st.AddMember(ctx, f.public.ID, f.bob.ID, store.PermissionWrite); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.ForRepository(ctx, f.bob, f.public); got != Write {
		t.Fatalf("write member on public repo = %v, want Write", got)
	}
	if err := f.st.RemoveMember(ctx, f.public.ID, f.bob.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.svc.ForRepository(ctx, f.bob, f.public); got != Read {
		t.Fatalf("after removal, member on public repo = %v, want Read", got)
	}
}

func TestCanPush(t *testing.T) {
	if !CanPush(Write, false) {
		t.Fatal("write level must be able to push")
	}
	if CanPush(Read, false) {
		t.Fatal("read level must not push")
	}
	if CanPush(Admin, true) {
		t.Fatal("archived repositories must reject pushes")
	}
	if CanPush(None, false) {
		t.Fatal("no access must not push")
	}
}
