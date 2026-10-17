package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newExecutor(t *testing.T) *Executor {
	t.Helper()
	e, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestVersion(t *testing.T) {
	e := newExecutor(t)
	ver, err := e.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if !strings.Contains(ver, ".") {
		t.Fatalf("version = %q, want dotted digits", ver)
	}
	if !AtLeast(ver, 2, 30) {
		t.Fatalf("host git %q is below the supported 2.30 floor", ver)
	}
}

func TestInitBareAndRevParse(t *testing.T) {
	e := newExecutor(t)
	dir := filepath.Join(t.TempDir(), "repo.git")

	if _, err := e.Run(context.Background(), Options{
		Args: []string{"init", "--bare", "--initial-branch=main", dir},
	}); err != nil {
		t.Fatalf("init --bare: %v", err)
	}
	res, err := e.Run(context.Background(), Options{
		Args: []string{"rev-parse", "--is-bare-repository"},
		Dir:  dir,
	})
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "true" {
		t.Fatalf("is-bare-repository = %q, want true", got)
	}
}

func TestSubcommandAllowlist(t *testing.T) {
	e := newExecutor(t)
	_, err := e.Run(context.Background(), Options{
		Args: []string{"gc"}, // known git subcommand, but not allowlisted
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("err = %v, want allowlist rejection", err)
	}
	_, err = e.Run(context.Background(), Options{Args: nil})
	if err == nil {
		t.Fatal("empty args must be rejected")
	}
}

func TestWorkingDirectoryMustBeAbsolute(t *testing.T) {
	e := newExecutor(t)
	_, err := e.Run(context.Background(), Options{
		Args: []string{"rev-parse", "--git-dir"},
		Dir:  "relative/path",
	})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("err = %v, want absolute-dir rejection", err)
	}
}

func TestHostConfigNeutralized(t *testing.T) {
	// A "hostile" global config that would leak into server-side behavior
	// if GIT_CONFIG_GLOBAL were not neutralized.
	fakeHome := t.TempDir()
	config := "[user]\n\tname = SHOULD_NOT_LEAK\n[i18n]\n\tlogOutputEncoding = latin1\n"
	if err := os.WriteFile(filepath.Join(fakeHome, ".gitconfig"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", fakeHome)
	t.Setenv("USERPROFILE", fakeHome)

	e := newExecutor(t)
	repo := filepath.Join(t.TempDir(), "r.git")
	if _, err := e.Run(context.Background(), Options{
		Args: []string{"init", "--bare", repo},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(context.Background(), Options{
		Args: []string{"config", "--get", "user.name"},
		Dir:  repo,
	})
	if err == nil {
		t.Fatalf("user.name unexpectedly resolvable: %q", res.Stdout)
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want ExitError", err)
	}
	if strings.Contains(string(res.Stdout), "SHOULD_NOT_LEAK") {
		t.Fatal("host global config leaked into the subprocess")
	}
}

func TestExtraEnvValidation(t *testing.T) {
	e := newExecutor(t)
	repo := filepath.Join(t.TempDir(), "r.git")
	init := Options{Args: []string{"init", "--bare", repo}}

	if _, err := e.Run(context.Background(), init); err != nil {
		t.Fatal(err)
	}

	// Allowlisted key with valid value must pass through.
	if _, err := e.Run(context.Background(), Options{
		Args: []string{"rev-parse", "--git-dir"},
		Dir:  repo,
		Env:  []string{"GIT_PROTOCOL=version=2"},
	}); err != nil {
		t.Fatalf("GIT_PROTOCOL=version=2 rejected: %v", err)
	}

	// Unknown key must be rejected.
	if _, err := e.Run(context.Background(), Options{
		Args: []string{"rev-parse", "--git-dir"},
		Dir:  repo,
		Env:  []string{"GIT_TRACE=1"},
	}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("err = %v, want env-key rejection", err)
	}

	// Bad GIT_PROTOCOL value must be rejected.
	if _, err := e.Run(context.Background(), Options{
		Args: []string{"rev-parse", "--git-dir"},
		Dir:  repo,
		Env:  []string{"GIT_PROTOCOL=version=99999999999999999999; rm -rf /"},
	}); err == nil || !strings.Contains(err.Error(), "invalid GIT_PROTOCOL") {
		t.Fatalf("err = %v, want GIT_PROTOCOL validation", err)
	}
}

func TestExitErrorCarriesStderr(t *testing.T) {
	e := newExecutor(t)
	repo := filepath.Join(t.TempDir(), "r.git")
	if _, err := e.Run(context.Background(), Options{Args: []string{"init", "--bare", repo}}); err != nil {
		t.Fatal(err)
	}
	_, err := e.Run(context.Background(), Options{
		Args: []string{"rev-parse", "--verify", "refs/heads/does-not-exist"},
		Dir:  repo,
	})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("err = %v, want ExitError", err)
	}
	if exitErr.Code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if len(exitErr.Stderr) == 0 {
		t.Fatal("ExitError.Stderr must capture git diagnostics")
	}
}

func TestContextCancellation(t *testing.T) {
	e := newExecutor(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Run(ctx, Options{Args: []string{"version"}})
	if err == nil {
		t.Fatal("cancelled context must produce an error")
	}
}
