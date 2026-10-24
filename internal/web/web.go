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
	"net/url"
	"strconv"
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

//go:embed templates/*.tmpl templates/partials/*.tmpl
var templateFS embed.FS

//go:embed static
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
	// InstanceSecret is the instance secret from data/secret.key,
	// used to encrypt TOTP secrets at rest.
	InstanceSecret string
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

	// Static assets served from fully-literal routes: a subtree pattern
	// would conflict with the /{owner}/{repo} wildcard (Go's ServeMux
	// panics on non-disjoint overlaps), so each asset gets its own route.
	for _, asset := range []struct{ route, file, ctype string }{
		{"/static/css/githost.css", "static/css/githost.css", "text/css; charset=utf-8"},
		{"/static/img/favicon.svg", "static/img/favicon.svg", "image/svg+xml"},
	} {
		blob, err := fs.ReadFile(staticFS, asset.file)
		if err != nil {
			return nil, err
		}
		mux.HandleFunc("GET "+asset.route, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", asset.ctype)
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(blob)
		})
	}

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
	mux.HandleFunc("GET /settings/password", h.secureHandler(h.passwordPage))
	mux.HandleFunc("POST /settings/password", h.secureHandler(h.passwordSubmit))
	mux.HandleFunc("GET /settings/mfa", h.secureHandler(h.mfaPage))
	mux.HandleFunc("POST /settings/mfa/enable", h.secureHandler(h.mfaEnable))
	mux.HandleFunc("POST /settings/mfa/disable", h.secureHandler(h.mfaDisable))
	mux.HandleFunc("GET /mfa/challenge", h.secureHandler(h.mfaChallenge))
	mux.HandleFunc("POST /mfa/verify", h.secureHandler(h.mfaChallengeVerify))

	// Global search.
	mux.HandleFunc("GET /search", h.secureHandler(h.searchPage))

	// Instance administration (admins only; 404 for everyone else).
	// Item routes use literal prefixes ("edit/", "delete/") — a bare
	// "/admin/users/{id}" wildcard would conflict with the repository
	// patterns like GET /{owner}/{repo}/tree and panic the mux.
	mux.HandleFunc("GET /admin/users", h.secureHandler(h.adminUsersPage))
	mux.HandleFunc("GET /admin/users/new", h.secureHandler(h.adminUserNewPage))
	mux.HandleFunc("POST /admin/users/new", h.secureHandler(h.adminUserCreate))
	mux.HandleFunc("GET /admin/users/edit/{id}", h.secureHandler(h.adminUserEditPage))
	mux.HandleFunc("POST /admin/users/edit/{id}", h.secureHandler(h.adminUserUpdate))
	mux.HandleFunc("POST /admin/users/edit/{id}/password", h.secureHandler(h.adminUserSetPassword))
	mux.HandleFunc("POST /admin/users/delete/{id}", h.secureHandler(h.adminUserDelete))

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
	mux.HandleFunc("GET /{owner}/{repo}/releases", h.secureHandler(h.releasesPage))
	mux.HandleFunc("GET /{owner}/{repo}/releases/new", h.secureHandler(h.releaseNew))
	mux.HandleFunc("POST /{owner}/{repo}/releases/new", h.secureHandler(h.releaseCreate))
	mux.HandleFunc("GET /{owner}/{repo}/settings", h.secureHandler(h.repoSettings))
	mux.HandleFunc("POST /{owner}/{repo}/settings", h.secureHandler(h.repoSettingsUpdate))
	mux.HandleFunc("POST /{owner}/{repo}/delete", h.secureHandler(h.repoDelete))

	// Repository labels (management).
	mux.HandleFunc("GET /{owner}/{repo}/settings/labels", h.secureHandler(h.labelsPage))
	mux.HandleFunc("POST /{owner}/{repo}/settings/labels/create", h.secureHandler(h.labelCreate))
	mux.HandleFunc("POST /{owner}/{repo}/settings/labels/delete", h.secureHandler(h.labelDelete))

	// Repository webhooks (management; deliveries recorded by the
	// internal/webhooks deliverer show up on the page).
	mux.HandleFunc("GET /{owner}/{repo}/settings/webhooks", h.secureHandler(h.webhooksPage))
	mux.HandleFunc("POST /{owner}/{repo}/settings/webhooks/create", h.secureHandler(h.webhookCreate))
	mux.HandleFunc("POST /{owner}/{repo}/settings/webhooks/toggle", h.secureHandler(h.webhookToggle))
	mux.HandleFunc("POST /{owner}/{repo}/settings/webhooks/delete", h.secureHandler(h.webhookDelete))

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

// parseTemplates precompiles every page as layout + partials + page,
// with a small function set shared by all templates.
func (h *Handler) parseTemplates() error {
	pages, err := fs.Glob(templateFS, "templates/*.tmpl")
	if err != nil {
		return err
	}
	partials, err := fs.Glob(templateFS, "templates/partials/*.tmpl")
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
		// dict builds a map for partial calls: (dict "Icon" "∅" "Title" "...").
		"dict": func(kv ...any) map[string]any {
			m := make(map[string]any, len(kv)/2)
			for i := 0; i+1 < len(kv); i += 2 {
				if k, ok := kv[i].(string); ok {
					m[k] = kv[i+1]
				}
			}
			return m
		},
		// colordot returns a CSP-safe SVG data URI dot in the label's
		// color (dynamic colors cannot live in the external stylesheet).
		"colordot": colorDotDataURI,
	}
	for _, page := range pages {
		name := strings.TrimPrefix(page, "templates/")
		if name == "layout.tmpl" {
			continue
		}
		patterns := make([]string, 0, len(partials)+2)
		patterns = append(patterns, "templates/layout.tmpl")
		patterns = append(patterns, partials...)
		patterns = append(patterns, page)
		tmpl, err := template.New("layout").Funcs(funcs).
			ParseFS(templateFS, patterns...)
		if err != nil {
			return fmt.Errorf("parse template %s: %w", name, err)
		}
		h.templates[name] = tmpl
	}
	return nil
}

// colorDotDataURI builds an inline SVG data URI with a rounded dot in
// the given 6-hex label color. Malformed colors fall back to a neutral
// gray. Only http(s) strings leave this function; the hex is filtered.
func colorDotDataURI(color string) template.URL {
	c := strings.ToLower(color)
	if len(c) != 6 {
		c = "8a94a6"
	}
	for _, r := range c {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			c = "8a94a6"
			break
		}
	}
	return template.URL(fmt.Sprintf(
		"data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' width='10' height='10'><circle cx='5' cy='5' r='4' fill='%%23%s'/></svg>", c))
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
		"repo-created":     "Repository created.",
		"repo-updated":     "Settings saved.",
		"repo-deleted":     "Repository deleted.",
		"key-added":        "SSH key added.",
		"key-removed":      "SSH key removed.",
		"token-revoked":    "Token revoked.",
		"password-changed": "Password updated. Please log in again.",
		"mfa-enabled":      "Two-factor authentication enabled.",
		"mfa-disabled":     "Two-factor authentication disabled.",
		"webhook-created":  "Webhook added.",
		"webhook-deleted":  "Webhook deleted.",
		"label-created":    "Label created.",
		"label-deleted":    "Label deleted.",
		"release-created":  "Release published.",
		"user-created":     "Account created.",
		"user-updated":     "Account updated.",
		"user-deleted":     "Account deleted.",
		"err-bad-csrf":     "The form expired. Please try again.",
		"err-forbidden":    "You are not allowed to do that.",
		"err-rate-login":   "Too many attempts. Try again in a few minutes.",
		"err-login":        "Invalid username or password.",
		"err-bad-request":  "Invalid input.",
		"err-merge":        "Merge failed. See the pull request page for details.",
	}
	return texts[key]
}

// requireSession returns the session data or writes a redirect to /login
// (GET) / 403 (POST) and reports false. A session still awaiting its
// second factor (mfa_pending) is redirected to the challenge instead:
// passing the password alone grants no page access.
func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request) (pageData, *store.SessionUser, bool) {
	pd, su, _ := h.pageData(r)
	if su == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return pd, nil, false
	}
	if su.Session.MFAPending {
		http.Redirect(w, r, "/mfa/challenge", http.StatusSeeOther)
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
// When the connection rides on TLS (directly or via the app's native
// TLS), HSTS instructs browsers to refuse future plain-HTTP connections.
func (h *Handler) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self'; img-src 'self' data:; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.TLS != nil {
			// 180 days, includeSubDomains, preload-eligible.
			w.Header().Set("Strict-Transport-Security",
				"max-age=15552000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) secureHandler(next http.HandlerFunc) http.HandlerFunc {
	return h.secureHeaders(next).ServeHTTP
}

// ---------------------------------------------------------------- helpers

// pageURLBuilder returns a closure rendering "?page=N" links that
// preserve the request's other query parameters (state filters, refs).
func pageURLBuilder(r *http.Request) func(int) template.URL {
	keep := r.URL.Query()
	keep.Del("page")
	return func(n int) template.URL {
		q := url.Values{}
		for k, vs := range keep {
			q[k] = vs
		}
		q.Set("page", strconv.Itoa(n))
		return template.URL(r.URL.Path + "?" + q.Encode())
	}
}

// pageParam reads a sanitized ?page= value (1..1000).
func pageParam(r *http.Request) int {
	page := 1
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n >= 1 && n <= 1000 {
		page = n
	}
	return page
}

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
