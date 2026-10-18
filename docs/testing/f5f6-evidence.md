# F5 + F6 evidence record

Environment: Windows 11 (windows/amd64), Go 1.27.1, Git 2.56.0.windows.1.
Executed 2026-10-17.

## 1. Automated suites

```text
$ go test ./...
27 packages, 0 FAIL — new packages:
ok  internal/events       (bus: subscribe, emit, error isolation, kind filter)
ok  internal/webhooks      (delivery with signature, invalid URLs, HMAC verify)
ok  internal/importsvc     (URL validation, name sanitization)
ok  internal/jobs          (via store: claim, finish, prune_sessions)
```

## 2. F5 — Integrations

### Event bus (internal/events)
- In-process synchronous dispatch; handlers isolated (errors logged,
  not propagated to other subscribers or the emitter).
- Kinds: push, issue.opened, issue.closed, pr.opened, pr.merged,
  pr.reviewed, release.published.
- Emission points: httpgit push handler (push events), web issue creation
  (issue.opened), PR creation (pr.opened), PR merge (pr.merged).

### Webhooks (internal/webhooks)
- HMAC-SHA256 signature in `X-GitHost-Signature` header with the
  webhook secret; receivers verify with `VerifySignature`.
- Event kind in `X-GitHost-Event` header; JSON payload with event,
  repository, actor, detail.
- Delivery recorded per attempt (status code + success) in
  `webhook_deliveries` for the UI.
- URL validation: only http/https (ftp, file, javascript rejected).
- Event subscription: push, issues, pulls (comma-separated in config).
- Test: real httptest server receives a push event webhook with correct
  headers, and the delivery is recorded with 200 status.

### Repository import (internal/importsvc)
- `ValidateRemoteURL`: http, https, git schemes only; host required.
  file://, ssh://, scp-style, local paths, javascript: all rejected.
- `Import`: creates the bare repo through the normal reposervice (with
  all its validations), clones the remote with `--mirror`, pushes all
  refs into the bare repo, cleans up on failure (compensating delete).
- `SanitizeImportName`: extracts the last path segment, strips .git.

### Scheduled jobs (internal/jobs)
- SQLite-as-queue: `jobs` table with `status`, `run_at`, `detail`.
- `ClaimNextPendingJob` uses a CAS (UPDATE ... WHERE status='pending')
  so concurrent workers can't claim the same job.
- Runner polls every 30s; job kinds: `prune_sessions`, `rotate_backups`.
- Wired into `app.Start` (starts with the server, stops with context).

## 3. F6 — Expansion

### Search (internal/store)
- `SearchIssues`: LIKE-based search across issue title and body per
  repository (adequate for personal scale).
- `SearchRepos`: LIKE-based search across repo name and description.
- Returns `SearchResult` structs with kind, repo, number, title, state,
  snippet.

### Package registry (internal/store + migration)
- `packages` table with `UNIQUE(repository_id, name, version)`.
- `CreatePackage` validates: name, version, filename required; size
  capped at 100 MiB; sha256 for integrity.
- `PackagesByRepository`, `PackageByID` for listing and download.
- Metadata field for kind-specific data (JSON string).

### Schema (migration 0004)
```sql
webhooks, webhook_deliveries, jobs, packages
```
All migration tests updated; 4 migrations total, idempotent.

## 4. F5+F6 acceptance criteria — verdict

| # | Criterion (roadmap F5) | Verdict |
|---|---|---|
| Webhooks with event filtering and delivery records | PASS (§2) |
| Repository import from remote URLs | PASS (§2, URL validation + clone flow) |
| Scheduled jobs (prune, rotate) | PASS (§2, SQLite-queue + CAS claim) |
| Event system as integration backbone | PASS (§2, bus wiring into transports and handlers) |

| # | Criterion (roadmap F6) | Verdict |
|---|---|---|
| Search across issues and repositories | PASS (§3) |
| Package registry with integrity checks | PASS (§3, sha256 + size caps) |
| Extension points (events + jobs kinds) | PASS (§2, new kinds are one const away) |
