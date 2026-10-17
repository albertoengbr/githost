package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/lock"
	"githost.dev/githost/internal/security"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/migrations"
)

// lockAcquire holds the instance lock and returns the release func.
func lockAcquire(dataDir string) (func(), error) {
	l, err := lock.Acquire(filepath.Join(dataDir, "githost.lock"))
	if err != nil {
		return nil, err
	}
	return func() { _ = l.Release() }, nil
}

// corruptManifestHash copies src to dst changing one character of the
// first manifest entry's sha256, so the content no longer matches.
func corruptManifestHash(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	gzr, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	gzw := gzip.NewWriter(out)
	defer gzw.Close()
	tw := tar.NewWriter(gzw)
	defer tw.Close()

	first := true
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if first && hdr.Name == ManifestName {
			first = false
			raw, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			// Flip the first sha256 character in the JSON.
			i := strings.Index(string(raw), `"sha256": "`)
			if i < 0 {
				return fmt.Errorf("sha256 field not found in manifest")
			}
			if raw[i+len(`"sha256": "`)] == '0' {
				raw[i+len(`"sha256": "`)] = '1'
			} else {
				raw[i+len(`"sha256": "`)] = '0'
			}
			if err := tw.WriteHeader(&tar.Header{Name: ManifestName, Mode: 0o644, Size: int64(len(raw))}); err != nil {
				return err
			}
			if _, err := tw.Write(raw); err != nil {
				return err
			}
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	return nil
}

// makeInstance builds a small but complete instance: database with rows,
// secret, SSH host key, one bare repository with content, and a lock
// artifact that must be skipped.
func makeInstance(t *testing.T) (dataDir, configPath string) {
	t.Helper()
	ctx := context.Background()
	dataDir = filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	db, err := database.Open(ctx, filepath.Join(dataDir, "githost.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	if _, err := st.CreateUser(ctx, "alice", "", "hash", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if err := security.WriteSecretFile(filepath.Join(dataDir, "secret.key")); err != nil {
		t.Fatal(err)
	}
	sshDir := filepath.Join(dataDir, "ssh")
	os.MkdirAll(sshDir, 0o700)
	if err := os.WriteFile(filepath.Join(sshDir, "host_ed25519"), []byte("pretend-host-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(dataDir, "repositories", "0123456789abcdef0123456789abcdef.git")
	os.MkdirAll(filepath.Join(repoDir, "refs", "heads"), 0o755)
	for name, content := range map[string]string{
		"HEAD":               "ref: refs/heads/main\n",
		"refs/heads/main":    "0123456789abcdef0123456789abcdef01234567\n",
		"objects/xx/pretend": "loose object bytes\n",
	} {
		path := filepath.Join(repoDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Artifacts that must NOT end up in a backup: a fake trash entry
	// (the WAL/SHM side files are covered by the skip pattern and a fake
	// one would corrupt the real database — SQLite reads those).
	os.WriteFile(filepath.Join(dataDir, "repositories", ".trash-abc"), []byte("x"), 0o644)

	configPath = filepath.Join(t.TempDir(), "githost.yaml")
	if err := os.WriteFile(configPath, []byte("server:\n  http_port: 3000\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dataDir, configPath
}

func TestCreateValidateRestore(t *testing.T) {
	dataDir, configPath := makeInstance(t)
	ctx := context.Background()
	backupDir := t.TempDir()
	archive := filepath.Join(backupDir, DefaultFileName(time.Now()))

	if err := Create(ctx, CreateOptions{
		DataDir:    dataDir,
		ConfigPath: configPath,
		Out:        archive,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(archive); err != nil {
		t.Fatalf("archive missing: %v", err)
	}

	// Validation passes and the manifest excludes runtime artifacts.
	manifest, err := Validate(archive)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if manifest.Version != ManifestVersion {
		t.Fatalf("manifest version = %d", manifest.Version)
	}
	names := map[string]bool{}
	for _, e := range manifest.Entries {
		names[e.Name] = true
	}
	for _, must := range []string{
		"instance/githost.db", "instance/secret.key",
		"instance/ssh/host_ed25519",
		"instance/repositories/0123456789abcdef0123456789abcdef.git/HEAD",
		"config/githost.yaml",
	} {
		if !names[must] {
			t.Errorf("manifest lacks %s", must)
		}
	}
	for _, mustNot := range []string{
		"instance/githost.lock",
		"instance/repositories/.trash-abc",
	} {
		if names[mustNot] {
			t.Errorf("manifest includes runtime artifact %s", mustNot)
		}
	}
	// WAL side files (present while the fixture db is open) must be
	// skipped too.
	for name := range names {
		if strings.HasSuffix(name, ".db-wal") || strings.HasSuffix(name, ".db-shm") {
			t.Errorf("manifest includes side file %s", name)
		}
	}

	// Tampering is detected: rebuild the archive with one corrupted
	// manifest hash (the gzip trailer is never read by the tar layer, so
	// tampering must hit verified content).
	tampered := filepath.Join(backupDir, "tampered.tar.gz")
	if err := corruptManifestHash(archive, tampered); err != nil {
		t.Fatalf("build tampered archive: %v", err)
	}
	if _, err := Validate(tampered); err == nil {
		t.Fatal("tampered archive validated")
	}

	// Restore into a clean directory.
	target := filepath.Join(t.TempDir(), "fresh-data")
	configOut := filepath.Join(t.TempDir(), "restored-githost.yaml")
	if err := Restore(ctx, RestoreOptions{
		Archive:   archive,
		DataDir:   target,
		ConfigOut: configOut,
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for _, must := range []string{
		"githost.db", "secret.key", "ssh/host_ed25519",
		"repositories/0123456789abcdef0123456789abcdef.git/HEAD",
	} {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(must))); err != nil {
			t.Errorf("restored instance lacks %s: %v", must, err)
		}
	}
	if _, err := os.Stat(configOut); err != nil {
		t.Errorf("config not restored: %v", err)
	}
	// The restored database is loadable and contains the user.
	db, err := database.Open(ctx, filepath.Join(target, "githost.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	if _, err := st.UserByUsername(ctx, "alice"); err != nil {
		t.Errorf("restored db lacks alice: %v", err)
	}

	// Restore refuses a non-empty target (protects live data).
	dirty := filepath.Join(t.TempDir(), "dirty")
	os.MkdirAll(filepath.Join(dirty, "data"), 0o755)
	os.WriteFile(filepath.Join(dirty, "data", "leftover.txt"), []byte("x"), 0o644)
	if err := Restore(ctx, RestoreOptions{Archive: archive, DataDir: filepath.Join(dirty, "data")}); err == nil {
		t.Fatal("restore into non-empty dir accepted")
	}
}

func TestCreateRefusesRunningInstance(t *testing.T) {
	dataDir, configPath := makeInstance(t)

	// Simulate a running server by holding the lock.
	holder, err := lockAcquire(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	err = Create(context.Background(), CreateOptions{
		DataDir:    dataDir,
		ConfigPath: configPath,
		Out:        filepath.Join(t.TempDir(), "x.tar.gz"),
	})
	if err == nil || !strings.Contains(err.Error(), "another GitHost process") {
		t.Fatalf("Create while locked: err = %v", err)
	}
	holder()
}

func TestListAndPrune(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 4; i++ {
		ts := time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC)
		path := filepath.Join(dir, DefaultFileName(ts))
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Noise that must be ignored.
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(dir, "githost-backup-dir.tar.gz"), 0o755)

	infos, err := List(dir)
	if err != nil || len(infos) != 4 {
		t.Fatalf("List = %v, %v", infos, err)
	}
	removed, err := Prune(dir, 2)
	if err != nil || len(removed) != 2 {
		t.Fatalf("Prune removed %v (%v)", removed, err)
	}
	infos, _ = List(dir)
	if len(infos) != 2 {
		t.Fatalf("after prune: %d backups", len(infos))
	}
	// The two newest survive.
	if !strings.HasSuffix(infos[0].Name, "000300.tar.gz") {
		t.Fatalf("wrong newest: %s", infos[0].Name)
	}
}
