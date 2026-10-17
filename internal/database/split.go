package database

import "strings"

// splitStatements splits a SQL script into individual statements.
//
// It understands the lexical elements that matter for GitHost migration
// files: single-quoted string literals (” is an escaped quote),
// double-quoted identifiers ("" is an escaped quote), line comments (--)
// and block comments (/* */). Semicolons inside those elements do not
// terminate a statement. Comment text is dropped from the returned
// statements. A trailing statement without a semicolon is returned as-is.
func splitStatements(script string) []string {
	var (
		stmts    []string
		cur      strings.Builder
		inSingle bool
		inDouble bool
		inLine   bool
		inBlock  bool
	)
	src := []byte(script)
	for i := 0; i < len(src); i++ {
		b := src[i]
		next := byte(0)
		if i+1 < len(src) {
			next = src[i+1]
		}
		switch {
		case inLine:
			if b == '\n' {
				inLine = false
				cur.WriteByte(b)
			}
		case inBlock:
			if b == '*' && next == '/' {
				inBlock = false
				i++
			}
		case inSingle:
			cur.WriteByte(b)
			if b == '\'' {
				if next == '\'' {
					cur.WriteByte(next)
					i++
				} else {
					inSingle = false
				}
			}
		case inDouble:
			cur.WriteByte(b)
			if b == '"' {
				if next == '"' {
					cur.WriteByte(next)
					i++
				} else {
					inDouble = false
				}
			}
		default:
			switch {
			case b == '-' && next == '-':
				inLine = true
				i++
			case b == '/' && next == '*':
				inBlock = true
				i++
			case b == '\'':
				inSingle = true
				cur.WriteByte(b)
			case b == '"':
				inDouble = true
				cur.WriteByte(b)
			case b == ';':
				if stmt := strings.TrimSpace(cur.String()); stmt != "" {
					stmts = append(stmts, stmt)
				}
				cur.Reset()
			default:
				cur.WriteByte(b)
			}
		}
	}
	if stmt := strings.TrimSpace(cur.String()); stmt != "" {
		stmts = append(stmts, stmt)
	}
	return stmts
}
