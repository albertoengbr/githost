package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------- webhooks

// Webhook is one registered outbound hook.
type Webhook struct {
	ID           int64
	RepositoryID int64
	URL          string
	Secret       string
	Events       string // comma-separated
	Active       bool
	CreatedAt    time.Time
}

// WebhookDelivery is one recorded delivery attempt.
type WebhookDelivery struct {
	ID         int64
	WebhookID  int64
	Event      string
	Payload    string
	StatusCode int
	Success    bool
	CreatedAt  time.Time
}

// CreateWebhook registers a webhook.
func (s *Store) CreateWebhook(ctx context.Context, repoID int64, webhookURL, secret, events string) (*Webhook, error) {
	if webhookURL == "" || len(webhookURL) > 2000 {
		return nil, fmt.Errorf("webhook url: required, up to 2000 characters")
	}
	if !strings.HasPrefix(webhookURL, "http://") && !strings.HasPrefix(webhookURL, "https://") {
		return nil, fmt.Errorf("webhook url: must start with http:// or https://")
	}
	for _, e := range strings.Split(events, ",") {
		switch strings.TrimSpace(e) {
		case "push", "issues", "pulls":
		default:
			return nil, fmt.Errorf("webhook event %q: must be push, issues or pulls", e)
		}
	}
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO webhooks (repository_id, url, secret, events, active, created_at) VALUES (?, ?, ?, ?, 1, ?)",
		repoID, webhookURL, secret, events, nowUTC())
	if err != nil {
		return nil, fmt.Errorf("create webhook: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Webhook{ID: id, RepositoryID: repoID, URL: webhookURL, Secret: secret, Events: events, Active: true}, nil
}

// WebhooksByRepository lists a repository's hooks.
func (s *Store) WebhooksByRepository(ctx context.Context, repoID int64) ([]*Webhook, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, repository_id, url, secret, events, active, created_at FROM webhooks WHERE repository_id = ? ORDER BY id", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hooks []*Webhook
	for rows.Next() {
		var h Webhook
		var active int
		var created string
		if err := rows.Scan(&h.ID, &h.RepositoryID, &h.URL, &h.Secret, &h.Events, &active, &created); err != nil {
			return nil, err
		}
		h.Active = active != 0
		h.CreatedAt = parseTime(created)
		hooks = append(hooks, &h)
	}
	return hooks, rows.Err()
}

// DeleteWebhook removes a hook.
func (s *Store) DeleteWebhook(ctx context.Context, repoID, hookID int64) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM webhooks WHERE id = ? AND repository_id = ?", hookID, repoID)
	return err
}

// SetWebhookActive toggles a webhook without deleting its history.
func (s *Store) SetWebhookActive(ctx context.Context, repoID, hookID int64, active bool) error {
	_, err := s.db.ExecContext(ctx,
		"UPDATE webhooks SET active = ? WHERE id = ? AND repository_id = ?",
		boolToInt(active), hookID, repoID)
	return err
}

// RecordWebhookDelivery stores one delivery attempt.
func (s *Store) RecordWebhookDelivery(ctx context.Context, hookID int64, event, payload string, statusCode int, success bool) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO webhook_deliveries (webhook_id, event, payload, status_code, success, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		hookID, event, payload, statusCode, boolToInt(success), nowUTC())
	return err
}

// RecentDeliveries returns the last N deliveries for a webhook.
func (s *Store) RecentDeliveries(ctx context.Context, hookID int64, limit int) ([]*WebhookDelivery, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, webhook_id, event, payload, status_code, success, created_at
		 FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT ?`, hookID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deliveries []*WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		var success int
		var created string
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.Event, &d.Payload, &d.StatusCode, &success, &created); err != nil {
			return nil, err
		}
		d.Success = success != 0
		d.CreatedAt = parseTime(created)
		deliveries = append(deliveries, &d)
	}
	return deliveries, rows.Err()
}

// ------------------------------------------------------------------- jobs

// Job is one scheduled task.
type Job struct {
	ID         int64
	Kind       string
	Status     string
	Detail     string
	RunAt      time.Time
	FinishedAt time.Time
	CreatedAt  time.Time
}

// EnqueueJob schedules a job.
func (s *Store) EnqueueJob(ctx context.Context, kind, detail string, runAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO jobs (kind, status, detail, run_at, created_at) VALUES (?, 'pending', ?, ?, ?)",
		kind, detail, runAt.UTC().Format(time.RFC3339), nowUTC())
	return err
}

// ClaimNextPendingJob atomically claims one pending job (or returns
// ErrNotFound when the queue is empty).
func (s *Store) ClaimNextPendingJob(ctx context.Context) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	row := tx.QueryRowContext(ctx,
		"SELECT id, kind, detail, run_at, created_at FROM jobs WHERE status = 'pending' ORDER BY run_at LIMIT 1")
	var j Job
	var runAt, createdAt string
	err = row.Scan(&j.ID, &j.Kind, &j.Detail, &runAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.RunAt = parseTime(runAt)
	j.CreatedAt = parseTime(createdAt)

	// CAS: only update if still pending.
	res, err := tx.ExecContext(ctx,
		"UPDATE jobs SET status = 'running' WHERE id = ? AND status = 'pending'", j.ID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound // someone else claimed it
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	j.Status = "running"
	return &j, nil
}

// FinishJob marks a job done (or failed).
func (s *Store) FinishJob(ctx context.Context, id int64, failed bool, detail string) error {
	status := "done"
	if failed {
		status = "failed"
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE jobs SET status = ?, detail = ?, finished_at = ? WHERE id = ?",
		status, detail, nowUTC(), id)
	return err
}

// RequeueStaleRunningJobs moves jobs stuck in the running state back to
// pending. The server holds the exclusive instance lock, so a running
// row at startup is a leftover from a killed previous run. Returns the
// number of rows requeued.
func (s *Store) RequeueStaleRunningJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"UPDATE jobs SET status = 'pending' WHERE status = 'running'")
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------- repo import

// ImportRepository clones a remote Git URL into a new GitHost repository.
// The executor's allowlist is bypassed for this one operation because
// the URL comes from the operator (not an anonymous user) — the CLI
// validates the URL shape before calling.
type ImportOptions struct {
	RemoteURL   string
	OwnerID     int64
	Name        string
	Description string
	Visibility  string
	// Mirror makes the imported repo read-only (pushes rejected).
	IsMirror bool
}

// ------------------------------------------------------------------ search

// SearchResult is one match.
type SearchResult struct {
	Kind    string // "issue" | "pull" | "repo"
	Repo    string
	Number  int64
	Title   string
	State   string
	Snippet string
}

// SearchIssues finds issues matching the query (simple LIKE search —
// adequate for personal scale).
func (s *Store) SearchIssues(ctx context.Context, repoID int64, query string, limit int) ([]SearchResult, error) {
	if query == "" || limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT i.number, i.title, i.state, substr(i.body_md, 1, 200)
		 FROM issues i WHERE i.repository_id = ? AND (i.title LIKE '%' || ? || '%' OR i.body_md LIKE '%' || ? || '%')
		 ORDER BY i.id DESC LIMIT ?`,
		repoID, query, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		r.Kind = "issue"
		if err := rows.Scan(&r.Number, &r.Title, &r.State, &r.Snippet); err != nil {
			return nil, err
		}
		results = append(results, r)
	}
	return results, rows.Err()
}

// SearchRepos finds repositories matching the query.
func (s *Store) SearchRepos(ctx context.Context, query string, limit int) ([]*Repository, error) {
	if query == "" || limit <= 0 || limit > 100 {
		limit = 50
	}
	return s.queryRepositories(ctx,
		"SELECT "+repositoryColumns+
			" FROM repositories r JOIN users u ON u.id = r.owner_id"+
			" WHERE r.name LIKE '%' || ? || '%' OR r.description LIKE '%' || ? || '%'"+
			" ORDER BY r.updated_at DESC LIMIT ?",
		query, query, limit)
}

// SearchIssuesAllRepos finds issues matching the query across every
// repository, filling Repo with "owner/name" so callers can apply
// per-repository authorization (visibility is NOT filtered here).
func (s *Store) SearchIssuesAllRepos(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	if query == "" || limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT u.username, r.name, i.number, i.title, i.state, substr(i.body_md, 1, 200)
		 FROM issues i
		 JOIN repositories r ON r.id = i.repository_id
		 JOIN users u ON u.id = r.owner_id
		 WHERE i.title LIKE '%' || ? || '%' OR i.body_md LIKE '%' || ? || '%'
		 ORDER BY i.id DESC LIMIT ?`,
		query, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var owner, name string
		r.Kind = "issue"
		if err := rows.Scan(&owner, &name, &r.Number, &r.Title, &r.State, &r.Snippet); err != nil {
			return nil, err
		}
		r.Repo = owner + "/" + name
		results = append(results, r)
	}
	return results, rows.Err()
}

// ------------------------------------------------------------------ packages

// Package is one registry entry.
type Package struct {
	ID           int64
	RepositoryID int64
	Name         string
	Version      string
	Kind         string
	Filename     string
	Size         int64
	SHA256       string
	StoragePath  string
	Metadata     string
	AuthorID     int64
	CreatedAt    time.Time
}

// CreatePackage records a package.
func (s *Store) CreatePackage(ctx context.Context, p Package) (*Package, error) {
	if p.Name == "" || p.Version == "" || p.Filename == "" {
		return nil, fmt.Errorf("package: name, version and filename are required")
	}
	if p.Size <= 0 || p.Size > 100<<20 {
		return nil, fmt.Errorf("package size: must be 1..%d bytes", 100<<20)
	}
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO packages (repository_id, name, version, kind, filename, size, sha256, storage_path, metadata, author_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.RepositoryID, p.Name, p.Version, p.Kind, p.Filename, p.Size, p.SHA256, p.StoragePath, p.Metadata, p.AuthorID, now)
	if err != nil {
		return nil, fmt.Errorf("create package: %w", err)
	}
	p.ID, _ = res.LastInsertId()
	p.CreatedAt = parseTime(now)
	return &p, nil
}

// PackagesByRepository lists a repository's packages.
func (s *Store) PackagesByRepository(ctx context.Context, repoID int64) ([]*Package, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, repository_id, name, version, kind, filename, size, sha256, storage_path, metadata, author_id, created_at
		 FROM packages WHERE repository_id = ? ORDER BY id DESC`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pkgs []*Package
	for rows.Next() {
		var p Package
		var created string
		if err := rows.Scan(&p.ID, &p.RepositoryID, &p.Name, &p.Version, &p.Kind,
			&p.Filename, &p.Size, &p.SHA256, &p.StoragePath, &p.Metadata, &p.AuthorID, &created); err != nil {
			return nil, err
		}
		p.CreatedAt = parseTime(created)
		pkgs = append(pkgs, &p)
	}
	return pkgs, rows.Err()
}

// PackageByID fetches one package.
func (s *Store) PackageByID(ctx context.Context, id int64) (*Package, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, repository_id, name, version, kind, filename, size, sha256, storage_path, metadata, author_id, created_at
		 FROM packages WHERE id = ?`, id)
	var p Package
	var created string
	err := row.Scan(&p.ID, &p.RepositoryID, &p.Name, &p.Version, &p.Kind,
		&p.Filename, &p.Size, &p.SHA256, &p.StoragePath, &p.Metadata, &p.AuthorID, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.CreatedAt = parseTime(created)
	return &p, nil
}
