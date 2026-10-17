// Package migrations embeds the versioned SQL schema migrations shipped in
// the GitHost binary. Files are named NNNN_name.sql and applied in order;
// see internal/database for the runner.
package migrations

import "embed"

// FS holds every *.sql migration, embedded at build time.
//
//go:embed *.sql
var FS embed.FS
