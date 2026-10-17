package database

import "testing"

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "empty",
			in:   "",
			want: nil,
		},
		{
			name: "two simple statements",
			in:   "CREATE TABLE a (id INTEGER);\nCREATE TABLE b (id INTEGER);\n",
			want: []string{"CREATE TABLE a (id INTEGER)", "CREATE TABLE b (id INTEGER)"},
		},
		{
			name: "comments are dropped",
			in:   "-- create a table\nCREATE TABLE a (id INTEGER); -- trailing\n-- done",
			want: []string{"CREATE TABLE a (id INTEGER)"},
		},
		{
			name: "semicolon inside string literal",
			in:   "INSERT INTO t VALUES ('a;b');\nINSERT INTO t VALUES ('c');",
			want: []string{"INSERT INTO t VALUES ('a;b')", "INSERT INTO t VALUES ('c')"},
		},
		{
			name: "escaped quote keeps string open",
			in:   "INSERT INTO t VALUES ('it''s;fine');",
			want: []string{"INSERT INTO t VALUES ('it''s;fine')"},
		},
		{
			name: "double-quoted identifier with semicolon",
			in:   `CREATE TABLE "weird;name" (id INTEGER);`,
			want: []string{`CREATE TABLE "weird;name" (id INTEGER)`},
		},
		{
			name: "block comment with semicolon",
			in:   "CREATE TABLE a (id INTEGER); /* semicolon ; inside */\nSELECT 1;",
			want: []string{"CREATE TABLE a (id INTEGER)", "SELECT 1"},
		},
		{
			name: "trailing statement without semicolon",
			in:   "SELECT 1;\nSELECT 2",
			want: []string{"SELECT 1", "SELECT 2"},
		},
		{
			name: "consecutive semicolons are ignored",
			in:   "SELECT 1;;\n;\nSELECT 2;",
			want: []string{"SELECT 1", "SELECT 2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitStatements(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d statements %q, want %d", len(got), got, len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("statement %d: got %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
