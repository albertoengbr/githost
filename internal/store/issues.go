package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ------------------------------------------------------------------- issues

// Issue is one tracker item.
type Issue struct {
	ID           int64
	RepositoryID int64
	Number       int64
	Title        string
	BodyMD       string
	State        string
	AuthorID     int64
	Author       string
	AssigneeID   int64 // 0 = unassigned
	Assignee     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Labels       []Label
	CommentCount int
}

// Label is a repository-scoped issue label.
type Label struct {
	ID    int64
	Name  string
	Color string
}

const issueColumns = `i.id, i.repository_id, i.number, i.title, i.body_md, i.state,
	i.author_id, u.username, i.assignee_id, IFNULL(a.username, ''), i.created_at, i.updated_at`

const issueJoins = ` FROM issues i
	JOIN users u ON u.id = i.author_id
	LEFT JOIN users a ON a.id = i.assignee_id`

func scanIssue(row interface{ Scan(...any) error }) (*Issue, error) {
	var it Issue
	var created, updated string
	var assigneeID sql.NullInt64
	err := row.Scan(&it.ID, &it.RepositoryID, &it.Number, &it.Title, &it.BodyMD, &it.State,
		&it.AuthorID, &it.Author, &assigneeID, &it.Assignee, &created, &updated)
	if err != nil {
		return nil, err
	}
	if assigneeID.Valid {
		it.AssigneeID = assigneeID.Int64
	}
	it.CreatedAt = parseTime(created)
	it.UpdatedAt = parseTime(updated)
	return &it, nil
}

// CreateIssue opens a new issue, allocating the next per-repository
// number atomically within the insert transaction.
func (s *Store) CreateIssue(ctx context.Context, repoID, authorID int64, title, body string) (*Issue, error) {
	if title == "" {
		return nil, fmt.Errorf("issue title: must not be empty")
	}
	if len(title) > 500 || len(body) > 200_000 {
		return nil, fmt.Errorf("issue: title or body exceeds size limits")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := nowUTC()
	var number int64
	if err := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(number), 0) + 1 FROM issues WHERE repository_id = ?", repoID).
		Scan(&number); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO issues (repository_id, number, title, body_md, state, author_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'open', ?, ?, ?)`,
		repoID, number, title, body, authorID, now, now)
	if err != nil {
		return nil, fmt.Errorf("create issue: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO issue_events (issue_id, actor_id, kind, detail, created_at) VALUES (?, ?, 'created', '', ?)",
		id, authorID, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.IssueByID(ctx, id)
}

// IssueByID fetches one issue with author/assignee joins.
func (s *Store) IssueByID(ctx context.Context, id int64) (*Issue, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+issueColumns+issueJoins+" WHERE i.id = ?", id)
	it, err := scanIssue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.hydrateIssue(ctx, it)
	return it, nil
}

// IssueByNumber resolves repository-local "#n".
func (s *Store) IssueByNumber(ctx context.Context, repoID, number int64) (*Issue, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+issueColumns+issueJoins+" WHERE i.repository_id = ? AND i.number = ?", repoID, number)
	it, err := scanIssue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.hydrateIssue(ctx, it)
	return it, nil
}

// Issues lists a repository's issues, optionally filtered by state
// (” = all), newest first.
func (s *Store) Issues(ctx context.Context, repoID int64, state string) ([]*Issue, error) {
	query := "SELECT " + issueColumns + issueJoins + " WHERE i.repository_id = ?"
	args := []any{repoID}
	if state != "" {
		query += " AND i.state = ?"
		args = append(args, state)
	}
	query += " ORDER BY i.id DESC"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var issues []*Issue
	for rows.Next() {
		it, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, it := range issues {
		s.hydrateIssue(ctx, it)
	}
	return issues, nil
}

func (s *Store) hydrateIssue(ctx context.Context, it *Issue) {
	labels, err := s.IssueLabels(ctx, it.ID)
	if err == nil {
		it.Labels = labels
	}
	_ = s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM issue_comments WHERE issue_id = ?", it.ID).Scan(&it.CommentCount)
}

// SetIssueState closes or reopens an issue, recording an event.
func (s *Store) SetIssueState(ctx context.Context, id, actorID int64, state string) error {
	if state != "open" && state != "closed" {
		return fmt.Errorf("issue state %q invalid", state)
	}
	kind := "closed"
	if state == "open" {
		kind = "reopened"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"UPDATE issues SET state = ?, updated_at = ? WHERE id = ?", state, nowUTC(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO issue_events (issue_id, actor_id, kind, detail, created_at) VALUES (?, ?, ?, '', ?)",
		id, actorID, kind, nowUTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// SetIssueAssignee assigns (or with 0 clears) the assignee, recording an event.
func (s *Store) SetIssueAssignee(ctx context.Context, id, actorID, assigneeID int64, assigneeName string) error {
	kind := "assigned"
	if assigneeID == 0 {
		kind = "unassigned"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"UPDATE issues SET assignee_id = ?, updated_at = ? WHERE id = ?",
		nilIfZero(assigneeID), nowUTC(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO issue_events (issue_id, actor_id, kind, detail, created_at) VALUES (?, ?, ?, ?, ?)",
		id, actorID, kind, assigneeName, nowUTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func nilIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// IssueComment is one comment on an issue.
type IssueComment struct {
	ID        int64
	AuthorID  int64
	Author    string
	BodyMD    string
	CreatedAt time.Time
}

// AddIssueComment appends a comment.
func (s *Store) AddIssueComment(ctx context.Context, issueID, authorID int64, body string) (*IssueComment, error) {
	if body == "" {
		return nil, fmt.Errorf("comment: must not be empty")
	}
	if len(body) > 100_000 {
		return nil, fmt.Errorf("comment: exceeds size limit")
	}
	now := nowUTC()
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO issue_comments (issue_id, author_id, body_md, created_at) VALUES (?, ?, ?, ?)",
		issueID, authorID, body, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	c := &IssueComment{ID: id, AuthorID: authorID, BodyMD: body, CreatedAt: parseTime(now)}
	if err := s.db.QueryRowContext(ctx,
		"SELECT username FROM users WHERE id = ?", authorID).Scan(&c.Author); err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE issues SET updated_at = ? WHERE id = ?", now, issueID); err != nil {
		return nil, err
	}
	return c, nil
}

// IssueComments lists an issue's comments oldest first.
func (s *Store) IssueComments(ctx context.Context, issueID int64) ([]*IssueComment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.author_id, u.username, c.body_md, c.created_at
		 FROM issue_comments c JOIN users u ON u.id = c.author_id
		 WHERE c.issue_id = ? ORDER BY c.id`, issueID)
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

// IssueEvent is one history entry.
type IssueEvent struct {
	ID        int64
	Actor     string
	Kind      string
	Detail    string
	CreatedAt time.Time
}

// IssueEvents returns the change history, oldest first.
func (s *Store) IssueEvents(ctx context.Context, issueID int64) ([]*IssueEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.id, IFNULL(u.username, 'system'), e.kind, e.detail, e.created_at
		 FROM issue_events e LEFT JOIN users u ON u.id = e.actor_id
		 WHERE e.issue_id = ? ORDER BY e.id`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []*IssueEvent
	for rows.Next() {
		var e IssueEvent
		var created string
		if err := rows.Scan(&e.ID, &e.Actor, &e.Kind, &e.Detail, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(created)
		events = append(events, &e)
	}
	return events, rows.Err()
}

// ------------------------------------------------------------------- labels

// CreateLabel adds a repository label.
func (s *Store) CreateLabel(ctx context.Context, repoID int64, name, color string) (*Label, error) {
	if len(name) == 0 || len(name) > 50 {
		return nil, fmt.Errorf("label name: must be 1-50 characters")
	}
	if len(color) != 6 || !isHexColor(color) {
		return nil, fmt.Errorf("label color: must be 6 hex characters")
	}
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO labels (repository_id, name, color, created_at) VALUES (?, ?, ?, ?)",
		repoID, name, color, nowUTC())
	if err != nil {
		return nil, fmt.Errorf("label %q: %w", name, err)
	}
	id, _ := res.LastInsertId()
	return &Label{ID: id, Name: name, Color: color}, nil
}

func isHexColor(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Labels lists a repository's labels.
func (s *Store) Labels(ctx context.Context, repoID int64) ([]Label, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, name, color FROM labels WHERE repository_id = ? ORDER BY name", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var labels []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.Name, &l.Color); err != nil {
			return nil, err
		}
		labels = append(labels, l)
	}
	return labels, rows.Err()
}

// DeleteLabel removes a label (and its issue associations, cascaded).
func (s *Store) DeleteLabel(ctx context.Context, repoID, labelID int64) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM labels WHERE id = ? AND repository_id = ?", labelID, repoID)
	return err
}

// AddIssueLabel associates a label, recording an event.
func (s *Store) AddIssueLabel(ctx context.Context, issueID, labelID, actorID int64) error {
	var name string
	if err := s.db.QueryRowContext(ctx, "SELECT name FROM labels WHERE id = ?", labelID).Scan(&name); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"INSERT OR IGNORE INTO issue_labels (issue_id, label_id) VALUES (?, ?)", issueID, labelID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO issue_events (issue_id, actor_id, kind, detail, created_at) VALUES (?, ?, 'labeled', ?, ?)",
		issueID, actorID, name, nowUTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveIssueLabel detaches a label, recording an event.
func (s *Store) RemoveIssueLabel(ctx context.Context, issueID, labelID, actorID int64) error {
	var name string
	if err := s.db.QueryRowContext(ctx, "SELECT name FROM labels WHERE id = ?", labelID).Scan(&name); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM issue_labels WHERE issue_id = ? AND label_id = ?", issueID, labelID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO issue_events (issue_id, actor_id, kind, detail, created_at) VALUES (?, ?, 'unlabeled', ?, ?)",
		issueID, actorID, name, nowUTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// IssueLabels lists an issue's labels.
func (s *Store) IssueLabels(ctx context.Context, issueID int64) ([]Label, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT l.id, l.name, l.color FROM issue_labels il
		 JOIN labels l ON l.id = il.label_id WHERE il.issue_id = ? ORDER BY l.name`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var labels []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.ID, &l.Name, &l.Color); err != nil {
			return nil, err
		}
		labels = append(labels, l)
	}
	return labels, rows.Err()
}
