# F2 evidence record

Environment: Windows 11 (windows/amd64), Go 1.27.1, Git 2.56.0.windows.1.
Executed 2026-10-17.

## 1. Automated suites

```text
$ go test ./...
ok  internal/api          (auth/scopes, repo lifecycle, listings, openapi sync)
ok  internal/app           (start/stop with the full route graph)
ok  internal/auth  internal/cli  internal/config  internal/csrf
ok  internal/database  internal/git  internal/httpserver  internal/logging
ok  internal/permissions  internal/ratelimit  internal/reposervice
ok  internal/security  internal/session  internal/storage  internal/store
ok  internal/web           (login/CSRF/ratelimit, repo pages, settings)
ok  tests/e2e              (F1 gates re-verified with the full mux)
```

## 2. Web UI (internal/web tests, real HTTP over httptest)

- **Login flow**: anonymous dashboard offers login; security headers
  (CSP, nosniff, X-Frame-Options, Referrer-Policy) on every page; POST
  without CSRF token → 400; wrong password → 401; correct login → 303
  and the session cookie renders the logged-in chrome.
- **Rate limiting (SEC-003)**: 5 failed logins lock the (ip, username)
  key; the 6th attempt — even with the correct password — answers 429.
- **Repository creation**: form + CSRF creates a private-by-default
  repository; duplicate name re-renders the form with a friendly
  "already in use" message (no raw SQL leaks).
- **Repo pages with real content** (seeded by a real client-side git
  push): home renders latest commit + README; tree listing; blob viewer;
  raw route streams exact bytes; commits page paginates; commit page
  shows the patch; branches page marks the default branch; tags page
  shows annotated tag messages.
- **Privacy**: anonymous and stranger sessions get 404 for private
  repositories; the owner sees them.
- **Settings pages**: SSH key added via form (fingerprint listed);
  token created via form reveals the plaintext exactly once (reload
  shows only the prefix); scopes stored as requested.

## 3. REST API (internal/api tests)

- Auth: no token → 401; invalid token → 401; `Bearer` accepted;
  `repo:write` required for creation (read-only token → 403).
- Repo lifecycle: create 201 (invalid/duplicate names → 400);
  owner GET 200; stranger GET on private → 404; stranger PATCH/DELETE
  on public → 403 (existence is public information there) and on
  private → 404; owner DELETE → 204 and repo gone.
- Refs & commits: branches, tags (annotated subjects), paginated commit
  history with `per_page` caps, all backed by the real git query layer.
- **Contract test**: `docs/api/openapi.yaml` is parsed and cross-checked
  against the registered route table — every route documented, every
  documented route registered. The test fails on drift in either
  direction.

## 4. Live binary validation

```text
$ githost init && admin create-user --username alice --password-stdin
$ githost admin create-token --username alice --scope repo:read --scope repo:write --scope admin
$ githost serve

GET /login          → 200, CSRF field present, CSP header present
POST /api/v1/repos  (token auth) → 201 {"repository":{"name":"api-made","visibility":"private"}}
GET  /api/v1/user/repos → 200, lists the created repo
GET  /api/v1/user   (no token)  → 401
GET /               (anonymous) → dashboard does NOT list the private repo;
                                   "Log in" link present
GET /static/css/githost.css → 200 (7.2 KB embedded asset)
git ls-remote http://alice:<token>@.../alice/api-made.git → connects (empty repo)
```

## 5. Engineering notes (findings worth keeping)

- Go's `ServeMux` panics on overlapping-but-incomparable patterns: a
  `/static/` subtree and `/{owner}/{repo}` conflict. The embedded
  stylesheet is therefore served from one fully-literal route.
- Inside `{{range}}` blocks, page-level fields must be reached via
  `$` — a mid-render template error otherwise truncates the page
  silently (caught by logging the render error).
- RFC3339 (second precision) timestamps make sub-second session TTLs
  untestable; expiry tests use pre-expired rows instead.
- CSRF tokens are HMAC(instance secret, binding) — stateless, bound to
  the session id (or the pre-login visitor cookie for the login form).

## 6. F2 acceptance criteria — verdict

| # | Criterion (roadmap F2) | Verdict |
|---|---|---|
| Dashboard, repository list, creation form | PASS (web tests + live) |
| File browser, file viewer, raw download | PASS |
| Commit history, commit detail with diff | PASS |
| Branches and tags pages | PASS |
| User profile, SSH keys, tokens pages | PASS |
| Repository settings (visibility/default branch/rename/archive/delete) | PASS |
| REST API `/api/v1` with OpenAPI contract | PASS (contract sync test) |
| All pages render real backend data | PASS (seeded real git content) |
| Authorization on every page/route | PASS (404-hides-private policy; negative tests) |

**F2 is ACCEPTED.** Markdown rendering of READMEs (currently plain
`<pre>`) and i18n move to later phases as documented limitations.
