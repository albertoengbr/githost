// Package e2e drives a real GitHost server with a real git client, the way
// an untrusted user would. The client-side git invocations here are
// deliberately NOT routed through internal/git: that package is the
// server-side engine gateway, while these commands play the role of the
// external attacker/user.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"githost.dev/githost/internal/app"
	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/config"
	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/storage"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/migrations"
)

// clientGit runs the client-side git binary in dir with an environment
// that never prompts and never inherits host configuration.
func clientGit(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		// "/dev/null" works on both POSIX and Git-for-Windows.
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=E2E Tester",
		"GIT_AUTHOR_EMAIL=e2e@example.test",
		"GIT_COMMITTER_NAME=E2E Tester",
		"GIT_COMMITTER_EMAIL=e2e@example.test",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// mustGit fails the test when the client git command fails.
func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := clientGit(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// wantGitFail asserts a client git command fails; returns its output.
func wantGitFail(t *testing.T, dir, wantInOutput string, args ...string) string {
	t.Helper()
	out, err := clientGit(t, dir, args...)
	if err == nil {
		t.Fatalf("git %s unexpectedly succeeded\n%s", strings.Join(args, " "), out)
	}
	if wantInOutput != "" && !strings.Contains(out, wantInOutput) {
		t.Fatalf("git %s failed as expected but output lacks %q:\n%s",
			strings.Join(args, " "), wantInOutput, out)
	}
	return out
}

// server is a live GitHost instance bound to an ephemeral port, with a
// second database handle for seeding.
type server struct {
	t       *testing.T
	cfg     *config.Config
	addr    string
	appSrv  *app.Server
	store   *store.Store
	reposvc *reposervice.Service
	repoDir string
}

func startServer(t *testing.T) *server {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()

	cfg := config.Default()
	if err := cfg.Resolve(dir); err != nil {
		t.Fatal(err)
	}
	cfg.Server.HTTPPort = 0 // ephemeral

	if err := app.Init(ctx, cfg); err != nil {
		t.Fatalf("app.Init: %v", err)
	}

	// Seed handle (separate connection; WAL supports it).
	db, err := database.Open(ctx, cfg.Database.SQLite.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Migrate(ctx, db, migrations.FS); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db)

	executor, err := git.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	repoDir := filepath.Join(dir, "data", "repositories")
	repoStorage, err := storage.NewRepositories(repoDir, executor)
	if err != nil {
		t.Fatal(err)
	}
	svc := reposervice.New(st, repoStorage)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	appSrv, err := app.Start(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("app.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		appSrv.Stop(stopCtx)
	})

	return &server{
		t:       t,
		cfg:     cfg,
		addr:    appSrv.Addr().String(),
		appSrv:  appSrv,
		store:   st,
		reposvc: svc,
		repoDir: repoDir,
	}
}

func (s *server) url() string {
	return "http://" + s.addr
}

var userSeq int64

// userWithToken creates a user and an access token, returning username and
// plaintext token.
func (s *server) userWithToken(scopes string) (string, string) {
	s.t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("u%04d", atomic.AddInt64(&userSeq, 1))
	user, err := s.store.CreateUser(ctx, name, "", "", false)
	if err != nil {
		s.t.Fatal(err)
	}
	token, tokenHash, _, err := auth.GenerateToken()
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := s.store.CreateToken(ctx, user.ID, "e2e", tokenHash, token[:10], scopes, nil); err != nil {
		s.t.Fatal(err)
	}
	return user.Username, token
}

func (s *server) createRepo(owner, name string, public bool) *store.Repository {
	s.t.Helper()
	visibility := store.VisibilityPrivate
	if public {
		visibility = store.VisibilityPublic
	}
	ctx := context.Background()
	ownerUser, err := s.store.UserByUsername(ctx, owner)
	if err != nil {
		s.t.Fatal(err)
	}
	repo, err := s.reposvc.Create(ctx, ownerUser, name, reposervice.CreateOptions{Visibility: visibility})
	if err != nil {
		s.t.Fatal(err)
	}
	return repo
}

// authURL builds http://user:token@host:port/path for git clients.
// Token values are hex (URL-safe); no escaping is needed.
func authURL(base, username, token, path string) string {
	host := strings.TrimPrefix(base, "http://")
	return "http://" + username + ":" + token + "@" + host + "/" + path
}

// barePath returns the on-disk bare repository directory of a repository.
func (s *server) barePath(repo *store.Repository) string {
	s.t.Helper()
	path, err := s.reposvc.DiskPath(repo)
	if err != nil {
		s.t.Fatal(err)
	}
	return path
}
