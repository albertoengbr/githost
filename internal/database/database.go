// Package database opens the SQLite metadata store and applies versioned
// SQL migrations.
//
// F0 stores all metadata in SQLite via the pure-Go driver
// modernc.org/sqlite (ADR-0001): no CGO is involved, which keeps
// cross-compilation for the whole support matrix possible.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// Open opens (creating when needed) the SQLite database at path and
// verifies connectivity. The parent directory of the database file is
// created when missing. Every pooled connection is configured with WAL
// journaling, a 5s busy timeout, NORMAL synchronous mode and foreign-key
// enforcement via driver pragmas in the DSN.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	// SQLite serializes writers; a small pool keeps concurrent readers
	// (WAL) while avoiding most "database is locked" contention.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", path, err)
	}
	return db, nil
}

// dsn builds the modernc.org/sqlite DSN. The path is encoded as a file URI
// so that characters such as spaces (common on Windows installs) are
// escaped correctly, and pragmas are attached as _pragma query options.
func dsn(path string) string {
	p := filepath.ToSlash(filepath.Clean(path))
	if !strings.HasPrefix(p, "/") {
		// Windows drive letters: F:/dir/file.db -> /F:/dir/file.db
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	q := url.Values{}
	for _, pragma := range []string{
		"journal_mode=WAL",
		"busy_timeout=5000",
		"synchronous=NORMAL",
		"foreign_keys=ON",
	} {
		q.Add("_pragma", pragma)
	}
	return u.String() + "?" + q.Encode()
}
