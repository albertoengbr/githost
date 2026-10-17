package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// Migration is one versioned SQL file, e.g. "0001_foundation.sql".
type Migration struct {
	Version int64
	Name    string
	Path    string
}

// migrationFile matches names like "0001_foundation.sql": a four-digit
// version, an underscore-separated lowercase name and the .sql suffix.
var migrationFile = regexp.MustCompile(`^(\d{4})_([a-z0-9]+(?:_[a-z0-9]+)*)\.sql$`)

// LoadMigrations lists the migrations in fsys sorted by version. Files that
// do not match the naming convention and duplicate versions are errors:
// migration files must be unambiguous and ordered.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var migs []Migration
	seen := make(map[int64]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := migrationFile.FindStringSubmatch(e.Name())
		if m == nil {
			return nil, fmt.Errorf("migrations: unexpected file %q (want NNNN_name.sql)", e.Name())
		}
		version, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("migrations: bad version in %q: %w", e.Name(), err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations: duplicate version %04d (%s and %s)", version, prev, e.Name())
		}
		seen[version] = e.Name()
		migs = append(migs, Migration{Version: version, Name: m[2], Path: e.Name()})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].Version < migs[j].Version })
	return migs, nil
}

// Migrate applies every pending migration in order and returns the versions
// that ran. Each migration executes inside a transaction; on failure the
// transaction rolls back and the error identifies the offending migration.
// A database that records a version missing from fsys is "dirty" (e.g. an
// instance downgraded from a newer build) and Migrate refuses to continue.
func Migrate(ctx context.Context, db *sql.DB, fsys fs.FS) ([]int64, error) {
	if err := ensureSchemaTable(ctx, db); err != nil {
		return nil, err
	}
	migs, err := LoadMigrations(fsys)
	if err != nil {
		return nil, err
	}
	available := make(map[int64]Migration, len(migs))
	for _, m := range migs {
		available[m.Version] = m
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}
	for version := range applied {
		if _, ok := available[version]; !ok {
			return nil, fmt.Errorf("migrations: database is dirty: applied version %04d is not present in this build", version)
		}
	}

	var ran []int64
	for _, m := range migs {
		if _, done := applied[m.Version]; done {
			continue
		}
		if err := applyMigration(ctx, db, m, fsys); err != nil {
			return nil, err
		}
		ran = append(ran, m.Version)
	}
	return ran, nil
}

// AppliedCount reports how many migrations are recorded in schema_migrations.
// It is used by `githost doctor`.
func AppliedCount(ctx context.Context, db *sql.DB) (int, error) {
	if err := ensureSchemaTable(ctx, db); err != nil {
		return 0, err
	}
	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return 0, err
	}
	return len(applied), nil
}

func ensureSchemaTable(ctx context.Context, db *sql.DB) error {
	const q = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	applied_at TEXT NOT NULL
)`
	if _, err := db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("migrations: create schema_migrations: %w", err)
	}
	return nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[int64]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("migrations: list applied: %w", err)
	}
	defer rows.Close()
	applied := make(map[int64]bool)
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("migrations: scan applied: %w", err)
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

func applyMigration(ctx context.Context, db *sql.DB, m Migration, fsys fs.FS) error {
	raw, err := fs.ReadFile(fsys, m.Path)
	if err != nil {
		return fmt.Errorf("migrations: read %s: %w", m.Path, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrations %04d_%s: begin: %w", m.Version, m.Name, err)
	}
	defer tx.Rollback() // no-op after a successful commit

	for i, stmt := range splitStatements(string(raw)) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrations %04d_%s: statement %d failed: %w", m.Version, m.Name, i+1, err)
		}
	}

	appliedAt := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
		m.Version, m.Name, appliedAt,
	); err != nil {
		return fmt.Errorf("migrations %04d_%s: record applied: %w", m.Version, m.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrations %04d_%s: commit: %w", m.Version, m.Name, err)
	}
	return nil
}
