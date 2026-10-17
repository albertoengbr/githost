// Package backup implements instance backup and restore.
//
// A backup is a single tar.gz containing a manifest with the SHA-256 of
// every file, a consistent SQLite snapshot (VACUUM INTO), the instance
// secret, SSH host keys, the bare repositories and (optionally) the
// configuration file. Creation and restore hold the instance lock, so
// they never race a running server; a crashed process releases the lock
// automatically (OS-level lock).
//
// Layout inside the archive:
//
//	manifest.json
//	config/githost.yaml            (when included)
//	instance/githost.db            (snapshot)
//	instance/secret.key
//	instance/ssh/...
//	instance/repositories/<uuid>.git/...
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/lock"
)

// ManifestName is the archive path of the manifest.
const ManifestName = "manifest.json"

// ManifestVersion guards the archive format.
const ManifestVersion = 1

// Entry describes one file in the archive.
type Entry struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Manifest is the first entry of every backup.
type Manifest struct {
	Version        int     `json:"version"`
	CreatedAt      string  `json:"created_at"`
	GitHostVersion string  `json:"githost_version"`
	Entries        []Entry `json:"entries"`
}

// DefaultFileName renders the conventional backup file name for a time.
func DefaultFileName(t time.Time) string {
	return "githost-backup-" + t.UTC().Format("20060102-150405") + ".tar.gz"
}

// CreateOptions describe one backup run.
type CreateOptions struct {
	DataDir    string // the instance data directory
	ConfigPath string // optional configuration file to include
	Out        string // the tarball to write
	Logger     *slog.Logger
}

// Create writes a verified tarball of the instance. The instance must not
// be running (it holds the same lock).
func Create(ctx context.Context, opts CreateOptions) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	instanceLock, err := lock.Acquire(lockPath(opts.DataDir))
	if err != nil {
		return err
	}
	defer instanceLock.Release()

	staging, err := os.MkdirTemp("", "githost-backup-")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	// 1. Consistent database snapshot.
	dbPath := filepath.Join(opts.DataDir, "githost.db")
	dbSnapshot := filepath.Join(staging, "githost.db")
	if err := snapshotDatabase(ctx, dbPath, dbSnapshot); err != nil {
		return err
	}

	// 2. Collect files (pass 1: hash everything).
	type file struct {
		name string // archive path
		path string // filesystem path
	}
	var files []file
	appendDir := func(root string, prefix string, skip func(rel string) bool) error {
		return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if skip != nil && skip(rel) {
				return nil
			}
			files = append(files, file{
				name: prefix + "/" + rel,
				path: path,
			})
			return nil
		})
	}

	// The snapshot replaces the live db file (which also skips -wal/-shm).
	if err := appendDir(opts.DataDir, "instance", func(rel string) bool {
		switch {
		case rel == "githost.lock",
			rel == "githost.db",
			strings.HasSuffix(rel, ".db-wal"),
			strings.HasSuffix(rel, ".db-shm"),
			rel == ".restore-staging":
			return true
		}
		// Trash from in-flight repository deletions (storage renames to
		// .trash-<uuid> before removing) never belongs in a backup.
		for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
			if strings.HasPrefix(seg, ".trash-") {
				return true
			}
		}
		return false
	}); err != nil {
		return fmt.Errorf("scan data directory: %w", err)
	}
	// The db snapshot joins under its archive name.
	files = append(files, file{name: "instance/githost.db", path: dbSnapshot})
	if opts.ConfigPath != "" {
		if _, err := os.Stat(opts.ConfigPath); err != nil {
			return fmt.Errorf("config file: %w", err)
		}
		files = append(files, file{name: "config/githost.yaml", path: opts.ConfigPath})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	manifest := Manifest{
		Version:   ManifestVersion,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Entries:   make([]Entry, 0, len(files)),
	}
	for _, f := range files {
		sum, size, err := hashFile(f.path)
		if err != nil {
			return fmt.Errorf("hash %s: %w", f.path, err)
		}
		manifest.Entries = append(manifest.Entries, Entry{Name: f.name, SHA256: sum, Size: size})
	}

	// 3. Write the archive: manifest first, then files (re-verified while
	// copied so a disk error cannot produce a silently corrupt backup).
	if err := os.MkdirAll(filepath.Dir(opts.Out), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	outFile, err := os.Create(opts.Out)
	if err != nil {
		return fmt.Errorf("create %s: %w", opts.Out, err)
	}
	defer outFile.Close()
	gzw := gzip.NewWriter(outFile)
	tw := tar.NewWriter(gzw)

	manifestJSON, err := json.MarshalIndent(&manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: ManifestName, Mode: 0o644, Size: int64(len(manifestJSON)),
	}); err != nil {
		return err
	}
	if _, err := tw.Write(manifestJSON); err != nil {
		return err
	}

	byName := make(map[string]Entry, len(manifest.Entries))
	for _, e := range manifest.Entries {
		byName[e.Name] = e
	}
	for _, f := range files {
		info, err := os.Stat(f.path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:    f.name,
			Mode:    0o644,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		}); err != nil {
			return err
		}
		src, err := os.Open(f.path)
		if err != nil {
			return err
		}
		hasher := sha256.New()
		n, err := io.Copy(io.MultiWriter(tw, hasher), src)
		src.Close()
		if err != nil {
			return fmt.Errorf("copy %s: %w", f.name, err)
		}
		got := hex.EncodeToString(hasher.Sum(nil))
		want := byName[f.name]
		if n != want.Size || got != want.SHA256 {
			return fmt.Errorf("file %s changed during backup (hash mismatch)", f.name)
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	if err := gzw.Close(); err != nil {
		return err
	}
	if err := outFile.Sync(); err != nil {
		return err
	}
	opts.Logger.Info("backup_created",
		"path", opts.Out,
		"files", len(manifest.Entries),
		"size_bytes", func() int64 { i, _ := os.Stat(opts.Out); return i.Size() }())
	return nil
}

// snapshotDatabase writes a consistent snapshot of the SQLite database
// into dst using VACUUM INTO.
func snapshotDatabase(ctx context.Context, dbPath, dst string) error {
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database %s: %w", dbPath, err)
	}
	db, err := database.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	// VACUUM INTO takes a literal filename; escape single quotes.
	literal := strings.ReplaceAll(dst, "'", "''")
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+literal+"'"); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	return nil
}

// hashFile streams a file, returning its hex sha256 and size.
func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func lockPath(dataDir string) string {
	return filepath.Join(dataDir, "githost.lock")
}

// ------------------------------------------------------------------ verify

// Validate checks that archivePath matches its own manifest: entry set,
// sizes and SHA-256 hashes. It returns the manifest on success.
func Validate(archivePath string) (*Manifest, error) {
	tr, closeAll, err := openArchive(archivePath)
	if err != nil {
		return nil, err
	}
	defer closeAll()

	// Manifest must be the first entry.
	hdr, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	if hdr.Name != ManifestName {
		return nil, fmt.Errorf("first archive entry is %q, want %q", hdr.Name, ManifestName)
	}
	raw, err := io.ReadAll(tr)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.Version != ManifestVersion {
		return nil, fmt.Errorf("unsupported manifest version %d", manifest.Version)
	}

	expect := make(map[string]Entry, len(manifest.Entries))
	for _, e := range manifest.Entries {
		expect[e.Name] = e
	}

	seen := make(map[string]bool, len(expect))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		want, ok := expect[hdr.Name]
		if !ok {
			return nil, fmt.Errorf("unexpected archive entry %q", hdr.Name)
		}
		if seen[hdr.Name] {
			return nil, fmt.Errorf("duplicate archive entry %q", hdr.Name)
		}
		seen[hdr.Name] = true

		h := sha256.New()
		n, err := io.Copy(h, tr)
		if err != nil {
			return nil, err
		}
		if n != want.Size {
			return nil, fmt.Errorf("entry %q: size %d, manifest says %d", hdr.Name, n, want.Size)
		}
		got := hex.EncodeToString(h.Sum(nil))
		if got != want.SHA256 {
			return nil, fmt.Errorf("entry %q: sha256 mismatch (archive is corrupt or was modified)", hdr.Name)
		}
	}
	for name := range expect {
		if !seen[name] {
			return nil, fmt.Errorf("entry %q listed in manifest is missing from the archive", name)
		}
	}
	return &manifest, nil
}

// openArchive opens a tar.gz for reading.
func openArchive(path string) (*tar.Reader, func() error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	gzr, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, nil, fmt.Errorf("gzip: %w", err)
	}
	tr := tar.NewReader(gzr)
	return tr, func() error {
		gzErr := gzr.Close()
		fErr := f.Close()
		if gzErr != nil {
			return gzErr
		}
		return fErr
	}, nil
}

// ------------------------------------------------------------------ restore

// RestoreOptions describe a restore.
type RestoreOptions struct {
	Archive   string // the tarball
	DataDir   string // target data directory (clean install)
	ConfigOut string // optional path to write the backed-up config to
	Logger    *slog.Logger
}

// Restore validates the archive and installs its contents into a clean
// data directory. The directory may not contain a previous instance
// (prevents accidental overwrites); the config file, when present, is
// written to ConfigOut instead of the data dir.
func Restore(ctx context.Context, opts RestoreOptions) error {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	instanceLock, err := lock.Acquire(lockPath(opts.DataDir))
	if err != nil {
		return err
	}
	defer instanceLock.Release()

	// The target must be a clean install.
	entries, err := os.ReadDir(opts.DataDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "githost.lock" {
			continue
		}
		return fmt.Errorf("data directory %s is not empty (restore targets a clean install; move the old data away first)", opts.DataDir)
	}

	if _, err := Validate(opts.Archive); err != nil {
		return fmt.Errorf("archive failed validation: %w", err)
	}

	// Extract inside the data dir (same volume → rename is atomic-ish).
	staging := filepath.Join(opts.DataDir, ".restore-staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	tr, closeAll, err := openArchive(opts.Archive)
	if err != nil {
		return err
	}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Name == ManifestName {
			continue
		}
		target, err := safeJoin(staging, hdr.Name)
		if err != nil {
			return err
		}
		switch {
		case hdr.Name == "config/githost.yaml" && opts.ConfigOut != "":
			if err := extractFile(tr, opts.ConfigOut, hdr.Size); err != nil {
				return err
			}
		default:
			if err := extractFile(tr, target, hdr.Size); err != nil {
				return fmt.Errorf("extract %s: %w", hdr.Name, err)
			}
		}
	}
	closeAll()

	// Move the instance payload into place.
	instance := filepath.Join(staging, "instance")
	payload, err := os.ReadDir(instance)
	if err != nil {
		return fmt.Errorf("archive has no instance payload: %w", err)
	}
	for _, e := range payload {
		src := filepath.Join(instance, e.Name())
		dst := filepath.Join(opts.DataDir, e.Name())
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("install %s: %w", e.Name(), err)
		}
	}
	opts.Logger.Info("backup_restored", "data_dir", opts.DataDir, "archive", opts.Archive)
	return nil
}

// safeJoin joins an archive path under base, rejecting traversal.
func safeJoin(base, name string) (string, error) {
	clean := filepath.Clean("/" + filepath.FromSlash(name)) // forces absolute
	target := filepath.Join(base, clean)
	if !strings.HasPrefix(target, filepath.Clean(base)+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry %q escapes the extraction root", name)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	return target, nil
}

// extractFile streams one tar entry into dst with a size guard.
func extractFile(tr *tar.Reader, dst string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, tr)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if size >= 0 && n != size {
		return fmt.Errorf("short read: got %d bytes, want %d", n, size)
	}
	return nil
}

// ------------------------------------------------------------------ listing

// Info describes a backup file.
type Info struct {
	Name      string
	Size      int64
	CreatedAt time.Time
}

// List returns the backups in dir (files matching the backup name
// pattern), newest first.
func List(dir string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "githost-backup-") ||
			!strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		infos = append(infos, Info{
			Name:      e.Name(),
			Size:      info.Size(),
			CreatedAt: info.ModTime(),
		})
	}
	// The name embeds the creation timestamp, so it is the deterministic
	// ordering key; modtime is only informational metadata.
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name > infos[j].Name })
	return infos, nil
}

// Prune keeps the newest keep backups in dir and deletes the rest.
// Returns the removed names.
func Prune(dir string, keep int) ([]string, error) {
	if keep < 0 {
		return nil, fmt.Errorf("keep must be >= 0")
	}
	infos, err := List(dir)
	if err != nil {
		return nil, err
	}
	var removed []string
	for i := keep; i < len(infos); i++ {
		if err := os.Remove(filepath.Join(dir, infos[i].Name)); err != nil {
			return removed, err
		}
		removed = append(removed, infos[i].Name)
	}
	return removed, nil
}
