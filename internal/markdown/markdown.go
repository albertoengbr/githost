// Package markdown renders user-authored Markdown into safe HTML.
//
// Security posture (spec §16, SEC-002): goldmark runs in its default
// SAFE mode — raw HTML is escaped, never executed. An AST transformer
// strips link/image destinations whose scheme is not allowlisted
// (blocks javascript:, data:, vbscript:, file:, ...). The output is
// safe to embed in templates as trusted HTML.
package markdown

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// safeLinkSchemes are the URL schemes Markdown links may use.
var safeLinkSchemes = map[string]bool{
	"http":   true,
	"https":  true,
	"mailto": true,
	"ftp":    true,
	"git":    true,
	"ssh":    true,
}

var engine = goldmark.New(
	goldmark.WithExtensions(
		extension.Strikethrough,
		extension.Table,
		extension.TaskList,
		extension.Linkify,
	),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		parser.WithASTTransformers(
			util.Prioritized(&linkSanitizer{}, 100),
		),
	),
	// No html.WithUnsafe(): raw HTML stays escaped (the default).
)

// Render converts Markdown to safe HTML.
func Render(source string) (template.HTML, error) {
	var buf bytes.Buffer
	if err := engine.Convert([]byte(source), &buf); err != nil {
		return "", fmt.Errorf("markdown: %w", err)
	}
	return template.HTML(buf.String()), nil //nolint:gosec // sanitized by construction (see package docs)
}

// linkSanitizer walks the parsed tree and clears unsafe destinations.
type linkSanitizer struct{}

func (s *linkSanitizer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	source := reader.Source()
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Link:
			if !SafeURL(string(t.Destination)) {
				t.Destination = nil
			}
		case *ast.AutoLink:
			if !SafeURL(string(t.URL(source))) {
				// Neutralize by pointing at an empty destination.
				t.Protocol = []byte("")
			}
		case *ast.Image:
			if !SafeURL(string(t.Destination)) {
				t.Destination = nil
			}
		}
		return ast.WalkContinue, nil
	})
}

// SafeURL reports whether a link destination is allowed. Exported for
// tests and potential reuse (e.g. comment previews).
func SafeURL(dest string) bool {
	dest = strings.TrimSpace(strings.ToLower(dest))
	if dest == "" {
		return false
	}
	// Relative references are fine.
	if strings.HasPrefix(dest, "/") || strings.HasPrefix(dest, "#") ||
		strings.HasPrefix(dest, "./") || strings.HasPrefix(dest, "../") {
		return true
	}
	// Scheme check: "javascript:alert(1)" has scheme javascript.
	// A pre-colon segment containing "@" or "." is scp-style host syntax
	// ("git@server:repo"), not a scheme — treat as safe.
	if idx := strings.IndexAny(dest, ":/?#"); idx >= 0 && dest[idx] == ':' {
		scheme := dest[:idx]
		if strings.ContainsAny(scheme, " \t\n\r") {
			return false
		}
		if strings.ContainsAny(scheme, "@.") {
			return true // scp-style / host:port form
		}
		return safeLinkSchemes[scheme]
	}
	return true
}
