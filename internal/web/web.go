// Package web serves GitHost's server-rendered interface: login, dashboard,
// repository browser and settings. Principles (spec §12): html/template with
// go:embed, zero JavaScript, responsive CSS with light/dark themes, and no
// visual imitation of GitHub/Gitea/Forgejo.
//
// Security: templates only receive narrow view structs (never store rows
// with password hashes); every mutating POST verifies a CSRF token bound
// to the session; login is rate-limited; private repositories render 404
// for unauthorized viewers.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"githost.dev/githost/internal/csrf"
	"githost.dev/githost/internal/events"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/prservice"
	"githost.dev/githost/internal/ratelimit"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/session"
	"githost.dev/githost/internal/store"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

//go:embed static/css/*.css
var staticFS embed.FS

// Deps wires the web interface to the service graph.
type Deps struct {
	Store    *store.Store
	Sessions *session.Manager
	CSRF     *csrf.Manager
	Perms    *permissions.Service
	Repos    *reposervice.Service
	Git      *git.Executor
	Limiter  *ratelimit.Limiter // login throttling
	Logger   *slog.Logger
	// Pulls is the PR service; nil disables the PR pages (only used
	// in tests that don't need collaboration features).
	Pulls *prservice.Service
	// Events is the instance event bus (F5); nil disables event emission.
	Events *events.Bus
}

// Handler serves the web UI.
type Handler struct {
	deps      Deps
	templates map[string]*template.Template
	logger    *slog.Logger
}

// Register mounts the web UI routes on mux and the embedded static assets.
func Register(mux *http.ServeMux, deps Deps) (*Handler, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	h := &Handler{
		deps:      deps,
		templates: make(map[string]*template.Template),
		logger:    deps.Logger,
	}
	if err := h.parseTemplates(); err != nil {
		return nil, err
	}

	// Static assets. The stylesheet is served from one fully-literal
	// route: a subtree pattern would conflict with the /{owner}/{repo}
	// wildcard (Go's ServeMux panics on non-disjoint overlaps), and there
	// is exactly one embedded asset in F2.
	css, err := fs.ReadFile(staticFS, "static/css/githost.css")
	if err != nil {
		return nil, err
	}
	mux.HandleFunc("GET /static/css/githost.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(css)
	})

	// Authentication.
	mux.HandleFunc("GET /login", h.secureHandler(h.loginPage))
	mux.HandleFunc("POST /login", h.secureHandler(h.loginSubmit))
	mux.HandleFunc("POST /logout", h.secureHandler(h.logout))

	// Dashboard and repository creation.
	mux.HandleFunc("GET /{$}", h.secureHandler(h.dashboard))
	mux.HandleFunc("GET /new", h.secureHandler(h.repoNewPage))
	mux.HandleFunc("POST /new", h.secureHandler(h.repoNewSubmit))

	// Personal settings.
	mux.HandleFunc("GET /settings/keys", h.secureHandler(h.keysPage))
	mux.HandleFunc("POST /settings/keys/add", h.secureHandler(h.keyAdd))
	mux.HandleFunc("POST /settings/keys/delete", h.secureHandler(h.keyDelete))
	mux.HandleFunc("GET /settings/tokens", h.secureHandler(h.tokensPage))
	mux.HandleFunc("POST /settings/tokens/create", h.secureHandler(h.tokenCreate))
	mux.HandleFunc("POST /settings/tokens/revoke", h.secureHandler(h.tokenRevoke))

	// Repository pages.
	mux.HandleFunc("GET /{owner}", h.secureHandler(h.profilePage))
	mux.HandleFunc("GET /{owner}/{repo}", h.secureHandler(h.repoHome))
	mux.HandleFunc("GET /{owner}/{repo}/tree", h.secureHandler(h.repoTree))
	mux.HandleFunc("GET /{owner}/{repo}/tree/{path...}", h.secureHandler(h.repoTree))
	mux.HandleFunc("GET /{owner}/{repo}/blob/{path...}", h.secureHandler(h.repoBlob))
	mux.HandleFunc("GET /{owner}/{repo}/raw/{path...}", h.secureHandler(h.repoRaw))
	mux.HandleFunc("GET /{owner}/{repo}/commits", h.secureHandler(h.repoCommits))
	mux.HandleFunc("GET /{owner}/{repo}/commit/{sha}", h.secureHandler(h.repoCommit))
	mux.HandleFunc("GET /{owner}/{repo}/branches", h.secureHandler(h.repoBranches))
	mux.HandleFunc("GET /{owner}/{repo}/tags", h.secureHandler(h.repoTags))
	mux.HandleFunc("GET /{owner}/{repo}/settings", h.secureHandler(h.repoSettings))
	mux.HandleFunc("POST /{owner}/{repo}/settings", h.secureHandler(h.repoSettingsUpdate))
	mux.HandleFunc("POST /{owner}/{repo}/delete", h.secureHandler(h.repoDelete))

	// Collaboration (F4).
	mux.HandleFunc("GET /{owner}/{repo}/issues", h.secureHandler(h.issueList))
	mux.HandleFunc("GET /{owner}/{repo}/issues/new", h.secureHandler(h.issueNew))
	mux.HandleFunc("POST /{owner}/{repo}/issues/new", h.secureHandler(h.issueNewSubmit))
	mux.HandleFunc("GET /{owner}/{repo}/issues/{number}", h.secureHandler(h.issueDetail))
	mux.HandleFunc("POST /{owner}/{repo}/issues/{number}/comment", h.secureHandler(h.issueComment))
	mux.HandleFunc("POST /{owner}/{repo}/issues/{number}/state", h.secureHandler(h.issueSetState))
	mux.HandleFunc("POST /{owner}/{repo}/issues/{number}/label", h.secureHandler(h.issueToggleLabel))
	mux.HandleFunc("GET /{owner}/{repo}/pulls", h.secureHandler(h.pullList))
	mux.HandleFunc("GET /{owner}/{repo}/pulls/new", h.secureHandler(h.pullNew))
	mux.HandleFunc("POST /{owner}/{repo}/pulls/new", h.secureHandler(h.pullNewSubmit))
	mux.HandleFunc("GET /{owner}/{repo}/pulls/{number}", h.secureHandler(h.pullDetail))
	mux.HandleFunc("POST /{owner}/{repo}/pulls/{number}/review", h.secureHandler(h.pullReview))
	mux.HandleFunc("POST /{owner}/{repo}/pulls/{number}/comment", h.secureHandler(h.pullComment))
	mux.HandleFunc("POST /{owner}/{repo}/pulls/{number}/merge", h.secureHandler(h.pullMerge))
	mux.HandleFunc("POST /{owner}/{repo}/pulls/{number}/state", h.secureHandler(h.pullSetState))

	return h, nil
}

// ------------------------------------------------------------ template engine

// parseTemplates precompiles every page as layout + page, with a small
// function set shared by all templates.
func (h *Handler) parseTemplates() error {
	pages, err := fs.Glob(templateFS, "templates/*.tmpl")
	if err != nil {
		return err
	}
	funcs := template.FuncMap{
		"timeago":  timeAgo,
		"bytes":    byteSize,
		"lower":    strings.ToLower,
		"joinpath": func(parts ...string) string { return strings.Join(parts, "/") },
		"Sub":      func(a, b int) int { return a - b },
		"Add":      func(a, b int) int { return a + b },
	}
	for _, page := range pages {
		name := strings.TrimPrefix(page, "templates/")
		if name == "layout.tmpl" {
			continue
		}
		tmpl, err := template.New("layout").Funcs(funcs).
			ParseFS(templateFS, "templates/layout.tmpl", page)
		if err != nil {
			return fmt.Errorf("parse template %s: %w", name, err)
		}
		h.templates[name] = tmpl
	}
	return nil
}

// render writes a page. The data must embed a pageData value (or be one).
func (h *Handler) render(w http.ResponseWriter, status int, page string, data any) {
	tmpl, ok := h.templates[page]
	if !ok {
		http.Error(w, "template missing: "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		// Headers are already sent; log and stop.
		h.logger.Error("template_render_failed", "template", page, "err", err)
	}
}

// ------------------------------------------------------------- view models

// pageData carries the values every template needs. Handlers embed it in
// their page-specific structs. User is a narrow view model — store rows
// with password hashes never reach templates.
type pageData struct {
	Username  string
	IsAdmin   bool
	CSRF      string
	Flash     string
	FlashKind string // "ok" | "err"
	Title     string // optional <title> suffix
}

// refresh populates the session-derived fields on every render.
func (h *Handler) pageData(r *http.Request) (pageData, *store.SessionUser, string) {
	token := session.FromRequest(r)
	su, err := h.deps.Sessions.Resolve(r.Context(), token)
	if err != nil {
		su = nil
	}
	binding := "anonymous"
	if su != nil {
		binding = su.Session.ID
	}
	pd := pageData{
		CSRF: h.deps.CSRF.Token(binding),
	}
	if su != nil {
		pd.Username = su.User.Username
		pd.IsAdmin = su.User.IsAdmin
	}
	// Flash messages ride in query strings; values are fixed strings
	// chosen by the handlers, never user-typed text.
	if msg := r.URL.Query().Get("msg"); msg != "" {
		pd.Flash = flashText(msg)
		pd.FlashKind = "ok"
		if strings.HasPrefix(msg, "err") {
			pd.FlashKind = "err"
		}
	}
	return pd, su, token
}

func flashText(key string) string {
	texts := map[string]string{
		"repo-created":    "Repository created.",
		"repo-updated":    "Settings saved.",
		"repo-deleted":    "Repository deleted.",
		"key-added":       "SSH key added.",
		"key-removed":     "SSH key removed.",
		"token-revoked":   "Token revoked.",
		"err-bad-csrf":    "The form expired. Please try again.",
		"err-forbidden":   "You are not allowed to do that.",
		"err-rate-login":  "Too many attempts. Try again in a few minutes.",
		"err-login":       "Invalid username or password.",
		"err-bad-request": "Invalid input.",
	}
	return texts[key]
}

// requireSession returns the session data or writes a redirect to /login
// (GET) / 403 (POST) and reports false.
func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request) (pageData, *store.SessionUser, bool) {
	pd, su, _ := h.pageData(r)
	if su == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return pd, nil, false
	}
	return pd, su, true
}

// verifyCSRF validates the submitted token against the session (or the
// anonymous binding for the login form).
func (h *Handler) verifyCSRF(r *http.Request, binding string) bool {
	token := r.PostFormValue("csrf")
	return h.deps.CSRF.Verify(binding, token)
}

// -------------------------------------------------------------- middleware

// secureHeaders adds the response hardening applied to every web page.
func (h *Handler) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) secureHandler(next http.HandlerFunc) http.HandlerFunc {
	return h.secureHeaders(next).ServeHTTP
}

// ---------------------------------------------------------------- helpers

func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n := int(d.Minutes())
		if n == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", n)
	case d < 24*time.Hour:
		n := int(d.Hours())
		if n == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", n)
	case d < 30*24*time.Hour:
		n := int(d.Hours() / 24)
		if n == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", n)
	}
	return t.UTC().Format("2006-01-02")
}

func byteSize(n int64) string {
	if n < 0 {
		return "—"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// clientIP extracts the remote address for rate limiting keys.
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		addr = addr[:i]
	}
	return addr
}

// repoRef returns the effective ref for a repository page: the query
// parameter when present, otherwise the repository's default branch.
func (h *Handler) repoRef(r *http.Request, repo *store.Repository) string {
	if ref := r.URL.Query().Get("ref"); ref != "" {
		return ref
	}
	return repo.DefaultBranch
}

// refList returns the page ref as a full ref name when it matches a known
// branch/tag, else passes it through (caller validates via git).
func fullRef(ref string) string {
	if ref == "HEAD" || strings.HasPrefix(ref, "refs/") || len(ref) == 40 || len(ref) == 64 {
		return ref
	}
	return "refs/heads/" + ref
}
