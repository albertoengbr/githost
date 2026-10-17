package git

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Query layer: read-only repository introspection used by the web UI and
// API. Everything goes through the allowlisted executor; every revision
// and tree path is validated before it reaches a subprocess argument.

// ---------------------------------------------------------------- revisions

// ValidateRevision accepts only the revision shapes GitHost consumes:
// "HEAD", a full object id (SHA-1 or SHA-256), or a full ref name
// (refs/...). Anything else — including option-looking strings — is
// rejected before a process is spawned.
func ValidateRevision(rev string) error {
	switch {
	case rev == "HEAD":
		return nil
	case len(rev) == 40 || len(rev) == 64:
		if isHex(rev) {
			return nil
		}
		return fmt.Errorf("revision %q: not a hexadecimal object id", rev)
	default:
		return ValidateRefPath(rev)
	}
}

// ValidateHash accepts only full object ids.
func ValidateHash(rev string) error {
	if (len(rev) == 40 || len(rev) == 64) && isHex(rev) {
		return nil
	}
	return fmt.Errorf("hash %q: not a valid object id", rev)
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ValidateTreePath accepts in-repository paths like "src/pkg/file.go":
// no leading slash, no "..", no empty segments, no NUL/newlines.
func ValidateTreePath(path string) error {
	if path == "" {
		return nil
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("tree path %q: must not start with '/'", path)
	}
	if strings.ContainsAny(path, "\x00\n") {
		return fmt.Errorf("tree path %q: invalid characters", path)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("tree path %q: invalid segment %q", path, seg)
		}
	}
	return nil
}

// ---------------------------------------------------------------------- refs

// Ref is a branch or tag entry.
type Ref struct {
	Full    string // refs/heads/main
	Short   string // main
	Target  string // object id
	Author  string // "Name <email>"
	Subject string
	When    time.Time
}

const eachRefFormat = "%(refname)%00%(objectname)%00%(authorname)%00%(authoremail)%00%(authordate:iso8601)%00%(contents:subject)%00"

// ListBranches returns the repository's branches, most recently authored
// first (the UI presentation order).
func (e *Executor) ListBranches(ctx context.Context, dir string) ([]Ref, error) {
	return e.listRefs(ctx, dir, "refs/heads")
}

// ListTags returns the repository's tags.
func (e *Executor) ListTags(ctx context.Context, dir string) ([]Ref, error) {
	return e.listRefs(ctx, dir, "refs/tags")
}

func (e *Executor) listRefs(ctx context.Context, dir, prefix string) ([]Ref, error) {
	res, err := e.Run(ctx, Options{
		Args: []string{"for-each-ref", "--format=" + eachRefFormat, prefix},
		Dir:  dir,
	})
	if err != nil {
		return nil, err
	}
	// Each record ends with the record separator followed by a newline;
	// parse line by line so the separator never leaks into the next
	// record's first field.
	var refs []Ref
	for _, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		fields := strings.Split(strings.TrimRight(line, "\x00"), "\x00")
		if len(fields) != 6 {
			continue
		}
		author := strings.TrimSpace(fields[2]) + " " + strings.TrimSpace(fields[3])
		refs = append(refs, Ref{
			Full:    fields[0],
			Short:   strings.TrimPrefix(fields[0], prefix+"/"),
			Target:  fields[1],
			Author:  strings.TrimSpace(author),
			Subject: fields[5],
			When:    parseGitTime(fields[4]),
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].When.After(refs[j].When) })
	return refs, nil
}

// -------------------------------------------------------------------- commits

// Commit is one log record.
type Commit struct {
	Hash           string
	Tree           string
	Parents        []string
	AuthorName     string
	AuthorEmail    string
	AuthorTime     time.Time
	CommitterName  string
	CommitterEmail string
	CommitTime     time.Time
	Subject        string
	Body           string
}

// IsMerge reports whether the commit has more than one parent.
func (c *Commit) IsMerge() bool { return len(c.Parents) > 1 }

const logFieldFormat = "%H%x1f%T%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%cn%x1f%ce%x1f%ct%x1f%s%x1f%b%x1e"

// Log returns up to limit commits reachable from rev, skipping `skip`
// entries (used for pagination).
func (e *Executor) Log(ctx context.Context, dir, rev string, skip, limit int) ([]Commit, error) {
	if err := ValidateRevision(rev); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	args := []string{
		"log",
		"-n", strconv.Itoa(limit),
		"--skip=" + strconv.Itoa(skip),
		"--format=" + logFieldFormat,
		rev,
	}
	res, err := e.Run(ctx, Options{Args: args, Dir: dir})
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, record := range strings.Split(strings.TrimRight(string(res.Stdout), "\x1e"), "\x1e") {
		record = strings.TrimLeft(record, "\n") // git's record separator is followed by \n
		if record == "" {
			continue
		}
		fields := strings.Split(record, "\x1f")
		if len(fields) != 11 {
			continue
		}
		var parents []string
		if fields[2] != "" {
			parents = strings.Fields(fields[2])
		}
		commits = append(commits, Commit{
			Hash:           fields[0],
			Tree:           fields[1],
			Parents:        parents,
			AuthorName:     fields[3],
			AuthorEmail:    fields[4],
			AuthorTime:     time.Unix(parseInt64(fields[5]), 0).UTC(),
			CommitterName:  fields[6],
			CommitterEmail: fields[7],
			CommitTime:     time.Unix(parseInt64(fields[8]), 0).UTC(),
			Subject:        strings.TrimRight(fields[9], "\n"),
			Body:           strings.TrimRight(fields[10], "\n"),
		})
	}
	return commits, nil
}

// Commit returns a single commit by hash.
func (e *Executor) Commit(ctx context.Context, dir, hash string) (*Commit, error) {
	if err := ValidateHash(hash); err != nil {
		return nil, err
	}
	commits, err := e.Log(ctx, dir, hash, 0, 1)
	if err != nil {
		return nil, err
	}
	if len(commits) == 0 {
		return nil, fmt.Errorf("commit %s: not found", hash)
	}
	return &commits[0], nil
}

// CommitPatch returns the textual patch of a commit (its diff against its
// first parent; root commits against the empty tree).
func (e *Executor) CommitPatch(ctx context.Context, dir, hash string) (string, error) {
	if err := ValidateHash(hash); err != nil {
		return "", err
	}
	res, err := e.Run(ctx, Options{
		Args: []string{"diff-tree", "--root", "-p", "--no-color", hash},
		Dir:  dir,
	})
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

// ---------------------------------------------------------------------- trees

// TreeEntry is one row of a directory listing.
type TreeEntry struct {
	Mode string
	Type string // blob | tree | commit (submodule)
	Hash string
	Size int64 // bytes; -1 for non-blobs (git prints "-")
	Name string
}

// LsTree lists one directory of the repository at a revision.
func (e *Executor) LsTree(ctx context.Context, dir, rev, treePath string) ([]TreeEntry, error) {
	if err := ValidateRevision(rev); err != nil {
		return nil, err
	}
	if err := ValidateTreePath(treePath); err != nil {
		return nil, err
	}
	spec := rev
	if treePath != "" {
		spec = rev + ":" + treePath
	}
	res, err := e.Run(ctx, Options{Args: []string{"ls-tree", "-l", spec}, Dir: dir})
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for _, line := range strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		meta, name, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 4 {
			continue
		}
		size := int64(-1)
		if fields[3] != "-" {
			if n, err := strconv.ParseInt(fields[3], 10, 64); err == nil {
				size = n
			}
		}
		entries = append(entries, TreeEntry{
			Mode: fields[0],
			Type: fields[1],
			Hash: fields[2],
			Size: size,
			Name: unquoteTreeName(name),
		})
	}
	return entries, nil
}

// unquoteTreeName strips the C-style quoting git applies to unusual names.
func unquoteTreeName(name string) string {
	if !strings.HasPrefix(name, "\"") {
		return name
	}
	if len(name) >= 2 && strings.HasSuffix(name, "\"") {
		return name[1 : len(name)-1]
	}
	return name
}

// ErrBlobTooLarge guards the file viewer against oversized previews.
type ErrBlobTooLarge struct{ Size int64 }

func (e *ErrBlobTooLarge) Error() string {
	return fmt.Sprintf("blob is %d bytes, above the preview limit", e.Size)
}

// CatFileBlob returns the content of one file at a revision, refusing
// blobs above maxBytes (the UI falls back to the raw download route).
func (e *Executor) CatFileBlob(ctx context.Context, dir, rev, path string, maxBytes int64) ([]byte, error) {
	if err := ValidateRevision(rev); err != nil {
		return nil, err
	}
	if err := ValidateTreePath(path); err != nil {
		return nil, err
	}
	spec := rev + ":" + path

	sizeRes, err := e.Run(ctx, Options{Args: []string{"cat-file", "-s", spec}, Dir: dir})
	if err != nil {
		return nil, err
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(sizeRes.Stdout)), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("blob size for %s: %w", path, err)
	}
	if maxBytes > 0 && size > maxBytes {
		return nil, &ErrBlobTooLarge{Size: size}
	}
	res, err := e.Run(ctx, Options{Args: []string{"cat-file", "blob", spec}, Dir: dir})
	if err != nil {
		return nil, err
	}
	return res.Stdout, nil
}

// IsBinary reports whether the content looks like a binary file (a NUL
// byte in the first 8 KiB — the heuristic git itself uses).
func IsBinary(data []byte) bool {
	const window = 8 * 1024
	if len(data) > window {
		data = data[:window]
	}
	return strings.ContainsRune(string(data), 0)
}

// ------------------------------------------------------------- default branch

// DefaultBranch returns the branch HEAD points at ("" for an empty repo
// where HEAD is unborn... actually git keeps the symbolic name even
// before the first commit, so it reports the configured initial branch).
func (e *Executor) DefaultBranch(ctx context.Context, dir string) (string, error) {
	res, err := e.Run(ctx, Options{Args: []string{"symbolic-ref", "HEAD"}, Dir: dir})
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.TrimSpace(string(res.Stdout)), "refs/heads/"), nil
}

// SetDefaultBranch points HEAD at the given branch.
func (e *Executor) SetDefaultBranch(ctx context.Context, dir, branch string) error {
	if err := ValidateBranchName(branch); err != nil {
		return err
	}
	_, err := e.Run(ctx, Options{
		Args: []string{"symbolic-ref", "HEAD", "refs/heads/" + branch},
		Dir:  dir,
	})
	return err
}

// ------------------------------------------------------------ parsing helpers

func parseGitTime(value string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05 -0700", strings.TrimSpace(value))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func parseInt64(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
