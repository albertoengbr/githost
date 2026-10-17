package markdown

import (
	"strings"
	"testing"
)

func TestBasics(t *testing.T) {
	out, err := Render("# Title\n\nSome **bold** and a [link](https://example.com).\n")
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	if !strings.Contains(html, "<h1") || !strings.Contains(html, "Title</h1>") {
		t.Fatalf("heading not rendered: %s", html)
	}
	if !strings.Contains(html, "<strong>bold</strong>") {
		t.Fatalf("bold not rendered: %s", html)
	}
	if !strings.Contains(html, `<a href="https://example.com"`) {
		t.Fatalf("link not rendered: %s", html)
	}
}

func TestRawHTMLIsOmitted(t *testing.T) {
	out, err := Render("<script>alert('xss')</script>\n\n<b>not really bold</b>")
	if err != nil {
		t.Fatal(err)
	}
	html := strings.ToLower(string(out))
	if strings.Contains(html, "<script>") || strings.Contains(html, "alert(") {
		t.Fatalf("script content leaked through: %s", out)
	}
	if !strings.Contains(html, "raw html omitted") {
		t.Fatalf("expected goldmark's raw-HTML omission marker: %s", out)
	}
}

func TestDangerousLinkSchemesStripped(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // must NOT appear
	}{
		{"javascript", `[click](javascript:alert(1))`, `href="javascript:`},
		{"data", `[x](data:text/html;base64,PHNjcmlwdD4=)`, `href="data:`},
		{"vbscript", `[x](vbscript:msgbox)`, `href="vbscript:`},
		{"file", `[x](file:///etc/passwd)`, `href="file:`},
		{"image javascript", `![x](javascript:alert(1))`, `src="javascript:`},
		{"mixed case", `[x](JaVaScRiPt:alert(1))`, `javascript:`},
		{"obfuscated whitespace", `[x](java\tscript:alert(1))`, `script:`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Render(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.ToLower(string(out)), tc.want) {
				t.Fatalf("unsafe destination leaked: %s", out)
			}
		})
	}
}

func TestSafeURL(t *testing.T) {
	safe := []string{
		"https://example.com", "http://x.test", "mailto:a@b.test",
		"/relative/path", "#fragment", "./local", "../up", "ssh://git@host/repo",
		"git@host:repo", "example.com/page", "readme.md",
	}
	for _, u := range safe {
		if !SafeURL(u) {
			t.Errorf("SafeURL(%q) = false, want true", u)
		}
	}
	unsafe := []string{
		"", "javascript:alert(1)", " data:text/html,x", "vbscript:x", "file:///x",
	}
	for _, u := range unsafe {
		if SafeURL(u) {
			t.Errorf("SafeURL(%q) = true, want false", u)
		}
	}
}

func TestTablesAndStrikethrough(t *testing.T) {
	out, err := Render("| a | b |\n|---|---|\n| 1 | 2 |\n\n~~gone~~\n")
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	if !strings.Contains(html, "<table>") {
		t.Fatalf("table not rendered: %s", html)
	}
	if !strings.Contains(html, "<del>gone</del>") {
		t.Fatalf("strikethrough not rendered: %s", html)
	}
}
