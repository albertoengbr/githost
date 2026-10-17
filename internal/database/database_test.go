package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	"githost.dev/githost/migrations"
)

// openTestDB opens a throwaway SQLite database, with a directory name that
// deliberately contains a space: Windows install paths frequently do, and
// the DSN must keep working there.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "githost data dir", "test.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenAppliesPragmas(t *testing.T) {
	db := openTestDB(t)

	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy_timeout = %d, want 5000", busyTimeout)
	}
}

func TestMigrateAppliesAndIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	ran, err := Migrate(ctx, db, migrations.FS)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if len(ran) != 2 || ran[0] != 1 || ran[1] != 2 {
		t.Fatalf("first run applied %v, want [1 2]", ran)
	}

	ran2, err := Migrate(ctx, db, migrations.FS)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(ran2) != 0 {
		t.Fatalf("second run applied %v, want none", ran2)
	}

	var value string
	if err := db.QueryRow("SELECT value FROM app_meta WHERE key = 'schema'").Scan(&value); err != nil {
		t.Fatalf("read app_meta: %v", err)
	}
	if value != "0001-foundation" {
		t.Fatalf("app_meta.schema = %q", value)
	}

	count, err := AppliedCount(ctx, db)
	if err != nil {
		t.Fatalf("AppliedCount: %v", err)
	}
	if count != 2 {
		t.Fatalf("applied count = %d, want 2", count)
	}
}

func TestMigrateRejectsDirtyDatabase(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := ensureSchemaTable(ctx, db); err != nil {
		t.Fatal(err)
	}
	const insertFake = "INSERT INTO schema_migrations (version, name, applied_at) VALUES (999, 'ghost', '1970-01-01T00:00:00Z')"
	if _, err := db.ExecContext(ctx, insertFake); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, db, migrations.FS); err == nil {
		t.Fatal("Migrate must refuse a database holding unknown versions")
	}
}

func TestMigrateFailureRollsBack(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	bad := fstest.MapFS{
		"0001_ok.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE ok_t (id INTEGER);")},
		"0002_bad.sql": &fstest.MapFile{Data: []byte("CREATE TABLE bad_t (id INTEGER);\nINSERT INTO missing_t VALUES (1);")},
	}
	if _, err := Migrate(ctx, db, bad); err == nil {
		t.Fatal("Migrate must fail on a broken migration")
	}

	// Neither the failed table nor its version record may survive.
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'bad_t'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("failed migration left its table behind")
	}
	if err := db.QueryRow("SELECT count(*) FROM schema_migrations WHERE version = 2").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("failed migration was recorded as applied")
	}
}

func TestLoadMigrationsValidation(t *testing.T) {
	cases := []struct {
		name string
		fsys fstest.MapFS
	}{
		{"bad file name", fstest.MapFS{"foo.sql": &fstest.MapFile{Data: []byte("")}}},
		{"uppercase name", fstest.MapFS{"0001_Bad.sql": &fstest.MapFile{Data: []byte("")}}},
		{"duplicate version", fstest.MapFS{
			"0001_a.sql": &fstest.MapFile{Data: []byte("")},
			"0001_b.sql": &fstest.MapFile{Data: []byte("")},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadMigrations(tc.fsys); err == nil {
				t.Fatal("want error")
			}
		})
	}

	good := fstest.MapFS{
		"0002_second.sql": &fstest.MapFile{Data: []byte("")},
		"0001_first.sql":  &fstest.MapFile{Data: []byte("")},
	}
	migs, err := LoadMigrations(good)
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) != 2 || migs[0].Version != 1 || migs[1].Version != 2 {
		t.Fatalf("migrations not ordered: %+v", migs)
	}
}
