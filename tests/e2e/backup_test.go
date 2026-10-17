package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"githost.dev/githost/internal/app"
	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/backup"
	"githost.dev/githost/internal/config"
)

// TestBackupRestoreGate is the F3 acceptance gate: a backup of a live
// instance, restored into a clean install, must produce a fully
// functional instance — same login, same tokens, same repositories,
// clone and push working. A restore that produces files without a
// working server is a failure by definition.
func TestBackupRestoreGate(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ---------------------------------------------------------- instance A
	s := startServer(t)
	alice, aliceToken := s.userWithToken(auth.ScopeRepoRead + " " + auth.ScopeRepoWrite)
	repo := s.createRepo(alice, "proj", false)

	// Give alice a real password so web login survives the restore.
	password := "restore-gate-password-1"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.SetPassword(ctx, mustUser(t, s, alice).ID, hash); err != nil {
		t.Fatal(err)
	}

	// Push real content into the repository (client-side pusher).
	work := t.TempDir()
	mustGit(t, work, "init", "--initial-branch=main", work)
	writeFile(t, work, "README.md", "# restore gate\noriginal content\n")
	mustGit(t, work, "add", "README.md")
	mustGit(t, work, "commit", "-m", "pre-backup commit")
	bare := s.barePath(repo)
	mustGit(t, work, "push", filepath.ToSlash(bare), "main")

	// Stop the server: backup requires the instance lock.
	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	if err := s.appSrv.Stop(stopCtx); err != nil {
		t.Fatalf("stop A: %v", err)
	}
	cancel()

	// ------------------------------------------------------------ backup
	backupDir := filepath.Join(t.TempDir(), "backups")
	archive := filepath.Join(backupDir, backup.DefaultFileName(time.Now()))
	if err := backup.Create(ctx, backup.CreateOptions{
		DataDir:    s.cfg.Paths.Data,
		ConfigPath: "",
		Out:        archive,
		Logger:     logger,
	}); err != nil {
		t.Fatalf("backup create: %v", err)
	}
	manifest, err := backup.Validate(archive)
	if err != nil {
		t.Fatalf("backup validate: %v", err)
	}
	if len(manifest.Entries) == 0 {
		t.Fatal("empty backup")
	}

	// Backup refuses to run while a server holds the lock (re-verified on
	// this instance's data dir with a freshly started server).
	srvHold, err := app.Start(ctx, s.cfg, logger)
	if err != nil {
		t.Fatalf("start holding server: %v", err)
	}
	err = backup.Create(ctx, backup.CreateOptions{
		DataDir: s.cfg.Paths.Data, Out: filepath.Join(backupDir, "nope.tar.gz"),
		Logger: logger,
	})
	if err == nil || !strings.Contains(err.Error(), "another GitHost process") {
		t.Fatalf("backup while running: err = %v", err)
	}
	stopCtx2, cancel2 := context.WithTimeout(ctx, 5*time.Second)
	if err := srvHold.Stop(stopCtx2); err != nil {
		t.Fatal(err)
	}
	cancel2()

	// ------------------------------------------------------ restore (clean)
	freshBase := t.TempDir()
	freshData := filepath.Join(freshBase, "data")
	configOut := filepath.Join(freshBase, "githost-restored.yaml")
	if err := backup.Restore(ctx, backup.RestoreOptions{
		Archive:   archive,
		DataDir:   freshData,
		ConfigOut: configOut,
		Logger:    logger,
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// ---------------------------------------------------------- instance B
	cfgB := config.Default()
	if err := cfgB.Resolve(freshBase); err != nil {
		t.Fatal(err)
	}
	if cfgB.Paths.Data != freshData {
		t.Fatalf("restored config paths mismatch: %s != %s", cfgB.Paths.Data, freshData)
	}
	cfgB.Server.HTTPPort = 0
	if err := app.Init(ctx, cfgB); err != nil {
		t.Fatalf("init restored instance (idempotent schema check): %v", err)
	}
	srvB, err := app.Start(ctx, cfgB, logger)
	if err != nil {
		t.Fatalf("start restored instance: %v", err)
	}
	stopB := func() {
		stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := srvB.Stop(stopCtx); err != nil {
			t.Errorf("stop B: %v", err)
		}
	}
	defer stopB()

	baseB := "http://" + srvB.Addr().String()
	bareB := filepath.Join(freshData, "repositories", repo.UUID+".git")

	// 1. The old token still authenticates Git over HTTPS (tokens were
	// restored, hash-only storage included).
	projURL := authURL(baseB, alice, aliceToken, alice+"/proj.git")
	cloneB := filepath.Join(t.TempDir(), "restored-clone")
	mustGit(t, t.TempDir(), "clone", projURL, cloneB)
	data, err := os.ReadFile(filepath.Join(cloneB, "README.md"))
	if err != nil {
		t.Fatalf("restored clone missing README: %v", err)
	}
	if string(data) != "# restore gate\noriginal content\n" {
		t.Fatalf("restored content mismatch: %q", data)
	}

	// 2. Pushing to the restored repository works and history is intact
	// in the restored instance's own bare repo.
	writeFile(t, cloneB, "post-restore.txt", "pushed after restore\n")
	mustGit(t, cloneB, "add", "post-restore.txt")
	mustGit(t, cloneB, "commit", "-m", "post-restore commit")
	mustGit(t, cloneB, "push", "origin", "main")
	out := mustGit(t, bareB, "log", "--format=%s", "refs/heads/main")
	if !strings.Contains(out, "post-restore commit") || !strings.Contains(out, "pre-backup commit") {
		t.Fatalf("history after restore+push:\n%s", out)
	}
	if fsckOut := mustGit(t, bareB, "fsck", "--strict"); strings.Contains(fsckOut, "error") {
		t.Fatalf("fsck on restored repo:\n%s", fsckOut)
	}

	// 3. Web login works on the restored instance with the pre-backup
	// password (password hashes restored). The client keeps a cookie jar
	// so the pre-login visitor cookie (CSRF binding) survives the form
	// submission.
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	loginResp, err := client.Get(baseB + "/login")
	if err != nil {
		t.Fatal(err)
	}
	loginBody, _ := io.ReadAll(loginResp.Body)
	loginResp.Body.Close()
	csrf := csrfFromHTML(t, string(loginBody))

	form := strings.NewReader("csrf=" + urlQueryEscape(csrf) +
		"&username=" + urlQueryEscape(alice) +
		"&password=" + urlQueryEscape(password))
	postResp, err := client.Post(baseB+"/login", "application/x-www-form-urlencoded", form)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, postResp.Body)
	postResp.Body.Close()
	if postResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("restored-instance login status = %d", postResp.StatusCode)
	}

	// 4. The web UI renders the restored repository.
	resp, err := client.Get(baseB + "/" + alice + "/proj")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restored repo page status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "restore gate") {
		t.Fatal("restored README not rendered on the web page")
	}
}

// csrfFromHTML extracts the CSRF token from a rendered form.
func csrfFromHTML(t *testing.T, page string) string {
	t.Helper()
	const marker = `name="csrf" value="`
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatalf("csrf field missing from login page")
	}
	rest := page[i+len(marker):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		t.Fatal("malformed csrf field")
	}
	return rest[:end]
}

// urlQueryEscape is the minimal URL escaping needed for form bodies.
func urlQueryEscape(s string) string {
	replacer := strings.NewReplacer(
		"+", "%2B", "&", "%26", "=", "%3D", "%", "%25", " ", "%20",
		"\n", "%0A", "\r", "%0D", ":", "%3A", "/", "%2F", "?", "%3F",
		"#", "%23", "@", "%40", "<", "%3C", ">", "%3E", "\"", "%22",
	)
	return replacer.Replace(s)
}
