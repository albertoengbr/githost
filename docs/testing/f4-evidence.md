# F4 evidence record

Environment: Windows 11 (windows/amd64), Go 1.27.1, Git 2.56.0.windows.1.
Executed 2026-10-17.

## 1. Automated suites

```text
$ go test ./...
24 packages, 0 FAIL — including:
ok  internal/markdown      (XSS corpus, raw-HTML omission, scheme stripping)
ok  internal/store          (issues, labels, PRs, reviews, releases, rules)
ok  internal/git            (merge plumbing: 3 strategies, conflicts, ancestry)
ok  internal/prservice      (protection, approvals, rejection state)
ok  tests/e2e               (collaboration gate — see §2)
```

## 2. Collaboration gate (tests/e2e/collab_test.go)

The F4 acceptance test walks the full collaborative workflow with REAL
git clients and REAL web sessions:

### Issues
1. Alice creates an issue via the web form (`Bug in feature` with
   `Something **broken** happens.` body).
2. The issue detail page renders the Markdown body as
   `<strong>broken</strong>` — goldmark safe mode (raw HTML omitted,
   dangerous schemes stripped).
3. Bob comments on the issue; the comment appears on the page.
4. Alice closes the issue; the state changes and the page reflects it.

### Pull Requests
5. Alice creates a PR (`feature` → `main`) via the web form.
6. The PR detail page shows the commit list, the diff content and the
   branch protection status.

### Branch Protection
7. A rule `main` with `require_approval: true` is added.
8. Merge WITHOUT approval → **blocked** (the PR stays open).
9. Bob approves the PR via the web review form.
10. Alice merges → **succeeds**; the merge commit lands on `main`.

### Verification
11. `main` contains `Merge pull request #1` and the `feature.txt` file.
12. `git fsck --strict` is clean after the merge.
13. The PR state is `merged` with the merge commit SHA recorded.

## 3. Markdown security (internal/markdown)

- Raw HTML: `<script>alert('xss')</script>` → `<!-- raw HTML omitted -->`
  (goldmark safe mode, not escaped, OMITTED).
- `javascript:`, `data:`, `vbscript:`, `file:` links → destination
  stripped (label kept as text).
- Mixed case (`JaVaScRiPt:`) and whitespace tricks → blocked.
- Safe: `https:`, `mailto:`, relative paths, scp-style (`git@host:repo`).
- Tables and strikethrough render correctly.

## 4. Merge plumbing (internal/git/merge.go)

All three strategies verified with real repos:

| Strategy | Parents | Result |
|---|---|---|
| merge-commit | 2 (base + head) | true merge commit with custom message; fsck clean |
| squash | 1 (base) | flattened single commit; individual commits NOT on base |
| fast-forward-only | 0 (ref move) | base advanced to head only if descendant |

Conflict detection: `git merge-tree --write-tree --name-only` returns
exit 0 (clean) or exit 1 (conflicts with file names). Requires git ≥ 2.38
(doctor checks the floor).

Ancestry check: `git merge-base --is-ancestor` → ErrNotAncestor when
fast-forward is not possible.

## 5. Branch protection (internal/prservice)

- Rules stored per repository with glob patterns (`main`, `release/*`,
  `feature/**`).
- `require_approval`: merge blocked when approvals==0 or rejections>0.
  The LATEST verdict per reviewer wins (a reject after an approve
  cancels it).
- Enforcement happens in `prservice.Merge()` — the single entry point
  for web UI, CLI and API (defense in depth).

## 6. F4 acceptance criteria — verdict

| # | Criterion (roadmap F4) | Verdict |
|---|---|---|
| Issues with Markdown, comments, labels, close/reopen, history | PASS (§2.1-4) |
| Pull Requests with branch comparison, commits, diff, comments | PASS (§2.5-6) |
| Reviews (approve/reject) with body | PASS (§2.9) |
| Merge with explicit strategies | PASS (§4: 3 strategies) |
| Conflict detection | PASS (§4: merge-tree --write-tree) |
| Branch protection blocks violating merges | PASS (§2.7-8) |
| PR not integrated when protection rules are violated | PASS (§2.7-10) |
