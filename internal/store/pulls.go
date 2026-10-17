package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------- pull requests

// PullRequest is a same-repository merge request (base/head are branch
// short names in the repository's bare repo).
type PullRequest struct {
	ID           int64
	RepositoryID int64
	Number       int64
	Title        string
	BodyMD       string
	BaseBranch   string
	HeadBranch   string
	State        string // open | merged | closed
	MergeCommit  string
	AuthorID     int64
	Author       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

const pullColumns = `p.id, p.repository_id, p.number, p.title, p.body_md,
	p.base_branch, p.head_branch, p.state, IFNULL(p.merge_commit, ''),
	p.author_id, u.username, p.created_at, p.updated_at`

func scanPull(row interface{ Scan(...any) error }) (*PullRequest, error) {
	var p PullRequest
	var created, updated string
	err := row.Scan(&p.ID, &p.RepositoryID, &p.Number, &p.Title, &p.BodyMD,
		&p.BaseBranch, &p.HeadBranch, &p.State, &p.MergeCommit,
		&p.AuthorID, &p.Author, &created, &updated)
	if err != nil {
		return nil, err
	}
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return &p, nil
}

// CreatePullRequest opens a PR, validating branches differ.
func (s *Store) CreatePullRequest(ctx context.Context, repoID, authorID int64,
	title, body, baseBranch, headBranch string) (*PullRequest, error) {
	if title == "" {
		return nil, fmt.Errorf("pull request title: must not be empty")
	}
	if len(title) > 500 || len(body) > 200_000 {
		return nil, fmt.Errorf("pull request: title or body exceeds size limits")
	}
	if baseBranch == "" || headBranch == "" {
		return nil, fmt.Errorf("pull request: base and head branches are required")
	}
	if baseBranch == headBranch {
		return nil, fmt.Errorf("pull request: base and head must differ")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := nowUTC()
	var number int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(number), 0) + 1 FROM pull_requests WHERE repository_id = ?", repoID).
		Scan(&number); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO pull_requests (repository_id, number, title, body_md, base_branch, head_branch,
			state, author_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, 'open', ?, ?, ?)`,
		repoID, number, title, body, baseBranch, headBranch, authorID, now, now)
	if err != nil {
		return nil, fmt.Errorf("create pull request: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.PullRequestByID(ctx, id)
}

// PullRequestByID fetches one PR.
func (s *Store) PullRequestByID(ctx context.Context, id int64) (*PullRequest, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+pullColumns+" FROM pull_requests p JOIN users u ON u.id = p.author_id WHERE p.id = ?", id)
	p, err := scanPull(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// PullRequestByNumber resolves repository-local PR number.
func (s *Store) PullRequestByNumber(ctx context.Context, repoID, number int64) (*PullRequest, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+pullColumns+
			" FROM pull_requests p JOIN users u ON u.id = p.author_id WHERE p.repository_id = ? AND p.number = ?",
		repoID, number)
	p, err := scanPull(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// PullRequests lists a repository's PRs, optionally by state.
func (s *Store) PullRequests(ctx context.Context, repoID int64, state string) ([]*PullRequest, error) {
	query := "SELECT " + pullColumns + " FROM pull_requests p JOIN users u ON u.id = p.author_id WHERE p.repository_id = ?"
	args := []any{repoID}
	if state != "" {
		query += " AND p.state = ?"
		args = append(args, state)
	}
	query += " ORDER BY p.id DESC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pulls []*PullRequest
	for rows.Next() {
		p, err := scanPull(rows)
		if err != nil {
			return nil, err
		}
		pulls = append(pulls, p)
	}
	return pulls, rows.Err()
}

// SetPullRequestState closes/reopens a PR (merge uses MarkMerged).
func (s *Store) SetPullRequestState(ctx context.Context, id int64, state string) error {
	if state != "open" && state != "closed" {
		return fmt.Errorf("pull request state %q invalid", state)
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE pull_requests SET state = ?, updated_at = ? WHERE id = ?", state, nowUTC(), id)
	return err
}

// MarkMerged records a successful merge.
func (s *Store) MarkMerged(ctx context.Context, id, mergedBy int64, mergeCommit string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE pull_requests SET state = 'merged', merge_commit = ?, merged_by = ?, updated_at = ?
		 WHERE id = ? AND state = 'open'`, mergeCommit, mergedBy, nowUTC(), id)
	return err
}

// -------------------------------------------------------------------- reviews

// ReviewVerdict values.
const (
	ReviewApprove = "approve"
	ReviewReject  = "reject"
	ReviewComment = "comment"
)

// PullRequestReview is one review verdict.
type PullRequestReview struct {
	ID         int64
	PullID     int64
	ReviewerID int64
	Reviewer   string
	Verdict    string
	BodyMD     string
	CreatedAt  time.Time
}

// AddPullRequestReview appends a review (approve/reject/comment).
func (s *Store) AddPullRequestReview(ctx context.Context, pullID, reviewerID int64, verdict, body string) (*PullRequestReview, error) {
	switch verdict {
	case ReviewApprove, ReviewReject, ReviewComment:
	default:
		return nil, fmt.Errorf("review verdict %q invalid", verdict)
	}
	if len(body) > 100_000 {
		return nil, fmt.Errorf("review body exceeds size limit")
	}
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO pull_request_reviews (pull_id, reviewer_id, verdict, body_md, created_at) VALUES (?, ?, ?, ?, ?)",
		pullID, reviewerID, verdict, body, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := s.db.ExecContext(ctx,
		"UPDATE pull_requests SET updated_at = ? WHERE id = ?", now, pullID); err != nil {
		return nil, err
	}
	var reviewer string
	if err := s.db.QueryRowContext(ctx,
		"SELECT username FROM users WHERE id = ?", reviewerID).Scan(&reviewer); err != nil {
		return nil, err
	}
	return &PullRequestReview{
		ID: id, PullID: pullID, ReviewerID: reviewerID, Reviewer: reviewer,
		Verdict: verdict, BodyMD: body, CreatedAt: parseTime(now),
	}, nil
}

// PullRequestReviews lists reviews oldest first.
func (s *Store) PullRequestReviews(ctx context.Context, pullID int64) ([]*PullRequestReview, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT r.id, r.pull_id, r.reviewer_id, u.username, r.verdict, r.body_md, r.created_at
		 FROM pull_request_reviews r JOIN users u ON u.id = r.reviewer_id
		 WHERE r.pull_id = ? ORDER BY r.id`, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reviews []*PullRequestReview
	for rows.Next() {
		var r PullRequestReview
		var created string
		if err := rows.Scan(&r.ID, &r.PullID, &r.ReviewerID, &r.Reviewer, &r.Verdict, &r.BodyMD, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = parseTime(created)
		reviews = append(reviews, &r)
	}
	return reviews, rows.Err()
}

// ApprovalState reports the count of outstanding approvals and the
// latest verdict of each reviewer (a later reject cancels an earlier
// approve).
func (s *Store) ApprovalState(ctx context.Context, pullID int64) (approvals int, rejections int, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT reviewer_id, verdict FROM pull_request_reviews
		 WHERE pull_id = ? ORDER BY id`, pullID)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	latest := map[int64]string{}
	for rows.Next() {
		var reviewer int64
		var verdict string
		if err := rows.Scan(&reviewer, &verdict); err != nil {
			return 0, 0, err
		}
		latest[reviewer] = verdict
	}
	for _, verdict := range latest {
		switch verdict {
		case ReviewApprove:
			approvals++
		case ReviewReject:
			rejections++
		}
	}
	return approvals, rejections, rows.Err()
}

// AddPullRequestComment appends a PR comment.
func (s *Store) AddPullRequestComment(ctx context.Context, pullID, authorID int64, body string) (*IssueComment, error) {
	if body == "" {
		return nil, fmt.Errorf("comment: must not be empty")
	}
	if len(body) > 100_000 {
		return nil, fmt.Errorf("comment: exceeds size limit")
	}
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO pull_request_comments (pull_id, author_id, body_md, created_at) VALUES (?, ?, ?, ?)",
		pullID, authorID, body, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	c := &IssueComment{ID: id, AuthorID: authorID, BodyMD: body, CreatedAt: parseTime(now)}
	if err := s.db.QueryRowContext(ctx,
		"SELECT username FROM users WHERE id = ?", authorID).Scan(&c.Author); err != nil {
		return nil, err
	}
	return c, nil
}

// PullRequestComments lists PR comments oldest first.
func (s *Store) PullRequestComments(ctx context.Context, pullID int64) ([]*IssueComment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.author_id, u.username, c.body_md, c.created_at
		 FROM pull_request_comments c JOIN users u ON u.id = c.author_id
		 WHERE c.pull_id = ? ORDER BY c.id`, pullID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var comments []*IssueComment
	for rows.Next() {
		var c IssueComment
		var created string
		if err := rows.Scan(&c.ID, &c.AuthorID, &c.Author, &c.BodyMD, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = parseTime(created)
		comments = append(comments, &c)
	}
	return comments, rows.Err()
}

// --------------------------------------------------------------- branch rules

// BranchRule protects matching branches.
type BranchRule struct {
	ID              int64
	RepositoryID    int64
	Pattern         string
	RequireApproval bool
	BlockForcePush  bool
	BlockDeletion   bool
}

// AddBranchRule stores a protection rule.
func (s *Store) AddBranchRule(ctx context.Context, rule BranchRule) (*BranchRule, error) {
	if rule.Pattern == "" || len(rule.Pattern) > 200 {
		return nil, fmt.Errorf("branch rule pattern: must be 1-200 characters")
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO branch_rules (repository_id, pattern, require_approval, block_force_push, block_deletion, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		rule.RepositoryID, rule.Pattern,
		boolToInt(rule.RequireApproval), boolToInt(rule.BlockForcePush), boolToInt(rule.BlockDeletion),
		nowUTC())
	if err != nil {
		return nil, err
	}
	rule.ID, _ = res.LastInsertId()
	return &rule, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// DeleteBranchRule removes a rule.
func (s *Store) DeleteBranchRule(ctx context.Context, repoID, ruleID int64) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM branch_rules WHERE id = ? AND repository_id = ?", ruleID, repoID)
	return err
}

// BranchRules lists a repository's rules.
func (s *Store) BranchRules(ctx context.Context, repoID int64) ([]*BranchRule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, repository_id, pattern, require_approval, block_force_push, block_deletion
		 FROM branch_rules WHERE repository_id = ? ORDER BY id`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []*BranchRule
	for rows.Next() {
		var r BranchRule
		var req, force, del int
		if err := rows.Scan(&r.ID, &r.RepositoryID, &r.Pattern, &req, &force, &del); err != nil {
			return nil, err
		}
		r.RequireApproval = req != 0
		r.BlockForcePush = force != 0
		r.BlockDeletion = del != 0
		rules = append(rules, &r)
	}
	return rules, rows.Err()
}

// ------------------------------------------------------------ releases/assets

// Release is a versioned publication attached to a tag.
type Release struct {
	ID           int64
	RepositoryID int64
	TagName      string
	Name         string
	NotesMD      string
	AuthorID     int64
	Author       string
	IsDraft      bool
	PublishedAt  time.Time // zero while draft
	CreatedAt    time.Time
}

// CreateRelease records a release (tag must exist in the repository —
// the service verifies that).
func (s *Store) CreateRelease(ctx context.Context, repoID, authorID int64,
	tag, name, notes string, draft bool) (*Release, error) {
	if tag == "" || len(tag) > 200 {
		return nil, fmt.Errorf("release tag: required, up to 200 characters")
	}
	if len(notes) > 500_000 {
		return nil, fmt.Errorf("release notes exceed size limit")
	}
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO releases (repository_id, tag_name, name, notes_md, author_id, is_draft, published_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoID, tag, name, notes, authorID, boolToInt(draft), publishedOr(draft, now), now)
	if err != nil {
		return nil, fmt.Errorf("create release: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.ReleaseByID(ctx, id)
}

func publishedOr(draft bool, now string) any {
	if draft {
		return nil
	}
	return now
}

// ReleaseByID fetches one release.
func (s *Store) ReleaseByID(ctx context.Context, id int64) (*Release, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT r.id, r.repository_id, r.tag_name, IFNULL(r.name, ''), r.notes_md, r.author_id,
		        u.username, r.is_draft, r.published_at, r.created_at
		 FROM releases r JOIN users u ON u.id = r.author_id WHERE r.id = ?`, id)
	var rel Release
	var draft int
	var created string
	var pubNull sql.NullString
	err := row.Scan(&rel.ID, &rel.RepositoryID, &rel.TagName, &rel.Name, &rel.NotesMD, &rel.AuthorID,
		&rel.Author, &draft, &pubNull, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rel.IsDraft = draft != 0
	if pubNull.Valid {
		rel.PublishedAt = parseTime(pubNull.String)
	}
	rel.CreatedAt = parseTime(created)
	return &rel, nil
}

// ReleasesByRepository lists releases newest first.
func (s *Store) ReleasesByRepository(ctx context.Context, repoID int64) ([]*Release, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT r.id, r.repository_id, r.tag_name, IFNULL(r.name, ''), r.notes_md, r.author_id,
		        u.username, r.is_draft, r.published_at, r.created_at
		 FROM releases r JOIN users u ON u.id = r.author_id
		 WHERE r.repository_id = ? ORDER BY r.id DESC`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var releases []*Release
	for rows.Next() {
		var rel Release
		var draft int
		var created string
		var pubNull sql.NullString
		if err := rows.Scan(&rel.ID, &rel.RepositoryID, &rel.TagName, &rel.Name, &rel.NotesMD, &rel.AuthorID,
			&rel.Author, &draft, &pubNull, &created); err != nil {
			return nil, err
		}
		rel.IsDraft = draft != 0
		if pubNull.Valid {
			rel.PublishedAt = parseTime(pubNull.String)
		}
		rel.CreatedAt = parseTime(created)
		releases = append(releases, &rel)
	}
	return releases, rows.Err()
}

// ReleaseAsset is one uploaded file.
type ReleaseAsset struct {
	ID          int64
	ReleaseID   int64
	Filename    string
	Size        int64
	SHA256      string
	StoragePath string
	CreatedAt   time.Time
}

// MaxAssetBytes caps uploads (checked by the caller; stored for the
// download page display).
const MaxAssetBytes = 100 << 20 // 100 MiB

// AddReleaseAsset records an uploaded asset row.
func (s *Store) AddReleaseAsset(ctx context.Context, releaseID int64, filename string,
	size int64, sha256, storagePath string) (*ReleaseAsset, error) {
	if filename == "" || len(filename) > 200 {
		return nil, fmt.Errorf("asset filename: required, up to 200 characters")
	}
	if size <= 0 || size > MaxAssetBytes {
		return nil, fmt.Errorf("asset size: must be 1..%d bytes", MaxAssetBytes)
	}
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO release_assets (release_id, filename, size, sha256, storage_path, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`, releaseID, filename, size, sha256, storagePath, now)
	if err != nil {
		return nil, fmt.Errorf("asset %q: %w", filename, err)
	}
	id, _ := res.LastInsertId()
	return &ReleaseAsset{
		ID: id, ReleaseID: releaseID, Filename: filename, Size: size,
		SHA256: sha256, StoragePath: storagePath, CreatedAt: parseTime(now),
	}, nil
}

// ReleaseAssets lists a release's assets.
func (s *Store) ReleaseAssets(ctx context.Context, releaseID int64) ([]*ReleaseAsset, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, release_id, filename, size, sha256, storage_path, created_at
		 FROM release_assets WHERE release_id = ? ORDER BY id`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var assets []*ReleaseAsset
	for rows.Next() {
		var a ReleaseAsset
		var created string
		if err := rows.Scan(&a.ID, &a.ReleaseID, &a.Filename, &a.Size, &a.SHA256, &a.StoragePath, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseTime(created)
		assets = append(assets, &a)
	}
	return assets, rows.Err()
}

// ReleaseAssetByID fetches one asset.
func (s *Store) ReleaseAssetByID(ctx context.Context, id int64) (*ReleaseAsset, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, release_id, filename, size, sha256, storage_path, created_at
		 FROM release_assets WHERE id = ?`, id)
	var a ReleaseAsset
	var created string
	err := row.Scan(&a.ID, &a.ReleaseID, &a.Filename, &a.Size, &a.SHA256, &a.StoragePath, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.CreatedAt = parseTime(created)
	return &a, nil
}
