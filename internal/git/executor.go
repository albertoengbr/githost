// Package git is the single gateway to the official git executable.
//
// Security invariants (docs/security/threat-model.md, threats T1/T8):
//   - arguments are structured argv slices; no shell is ever involved;
//   - only allowlisted subcommands can run;
//   - working directories must be absolute when provided;
//   - the subprocess environment is rebuilt from an allowlist of inherited
//     variables, and GIT_CONFIG_NOSYSTEM / GIT_CONFIG_GLOBAL neutralize host
//     configuration so server-side git behavior cannot be influenced from
//     the machine's config files;
//   - every invocation carries a context: cancellation kills the process.
//
// No other package in GitHost may import os/exec to run git.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultTimeout bounds a single git invocation when Options.Timeout is 0.
// Transports reuse it; nothing in GitHost may run git without a deadline.
const DefaultTimeout = 10 * time.Minute

// allowedSubcommands is the exhaustive list of git subcommands GitHost may
// execute. Anything else is rejected before a process is spawned. Phases
// extend this list only after their ADR/security review.
var allowedSubcommands = map[string]bool{
	"cat-file":         true,
	"check-ref-format": true,
	"commit-tree":      true,
	"config":           true, // constant keys only - never user-supplied keys
	"count-objects":    true,
	"diff-tree":        true,
	"for-each-ref":     true,
	"fsck":             true,
	"init":             true,
	"log":              true,
	"ls-tree":          true,
	"merge-base":       true,
	"merge-tree":       true,
	"receive-pack":     true,
	"rev-parse":        true,
	"symbolic-ref":     true,
	"update-ref":       true,
	"upload-pack":      true,
	"version":          true,
}

// allowedExtraEnv are the only extra environment variables callers may pass
// to git subprocesses.
var allowedExtraEnv = map[string]bool{
	// Requested by clients for protocol v2; validated by validateExtraEnv.
	"GIT_PROTOCOL": true,
}

var gitProtocolValue = regexp.MustCompile(`^version=[0-9]+$`)

// gitConfigEntry validates "key=value" pairs passed as `git -c` settings.
// Keys are dot-separated identifiers (git config keys are
// case-insensitive; camelCase like receive.maxInputSize is canonical).
// Values allow printable ASCII including spaces, '@' and ':' (email
// addresses, commit messages fragments) — argv passes them safely.
var gitConfigEntry = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*(\.[a-zA-Z][a-zA-Z0-9]*)+=[!-~]{1,128}$`)

// Executor runs the official git binary under strict constraints.
type Executor struct {
	bin    string
	logger *slog.Logger
}

// New resolves the git binary from PATH and returns an Executor. Fails when
// git is not installed — the server refuses to guess.
func New(logger *slog.Logger) (*Executor, error) {
	bin, err := exec.LookPath("git")
	if err != nil {
		return nil, fmt.Errorf("find git executable: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Executor{bin: bin, logger: logger}, nil
}

// Options describes one git invocation. Args[0] is the subcommand.
type Options struct {
	Args    []string
	Dir     string        // working directory; must be absolute when non-empty
	Env     []string      // extra "KEY=VALUE" entries; keys are allowlisted
	Config  []string      // "key=value" git -c settings; strictly validated
	Timeout time.Duration // zero means DefaultTimeout
	Stdin   io.Reader     // nil for no input
	Stdout  io.Writer     // nil: captured in Result; set: streamed (transports)
	Stderr  io.Writer     // nil: captured in Result; set: also captured
}

// Result carries captured output of an invocation.
type Result struct {
	Stdout []byte
	Stderr []byte
}

// ExitError reports a git process that ran but exited non-zero.
type ExitError struct {
	Subcommand string
	Code       int
	Stderr     []byte
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(string(e.Stderr))
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	if msg == "" {
		return fmt.Sprintf("git %s: exit status %d", e.Subcommand, e.Code)
	}
	return fmt.Sprintf("git %s: exit status %d: %s", e.Subcommand, e.Code, msg)
}

// Version returns the git version string (e.g. "2.56.0.windows.1").
func (e *Executor) Version(ctx context.Context) (string, error) {
	res, err := e.Run(ctx, Options{Args: []string{"version"}})
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimSpace(string(res.Stdout)))
	if len(fields) < 3 {
		return "", fmt.Errorf("git: unexpected version output %q", strings.TrimSpace(string(res.Stdout)))
	}
	return fields[2], nil
}

// Run executes one allowlisted git command. Output is captured unless the
// caller provides writers (used by the transports to stream pack data).
func (e *Executor) Run(ctx context.Context, opts Options) (*Result, error) {
	if len(opts.Args) == 0 {
		return nil, errors.New("git: empty command")
	}
	sub := opts.Args[0]
	if !allowedSubcommands[sub] {
		return nil, fmt.Errorf("git: subcommand %q is not allowed", sub)
	}
	if opts.Dir != "" && !filepath.IsAbs(opts.Dir) {
		return nil, fmt.Errorf("git: working directory must be absolute, got %q", opts.Dir)
	}
	for _, entry := range opts.Config {
		if !gitConfigEntry.MatchString(entry) {
			return nil, fmt.Errorf("git: invalid -c setting %q", entry)
		}
	}
	env, err := buildEnv(opts.Env)
	if err != nil {
		return nil, err
	}

	timeout := DefaultTimeout
	if opts.Timeout > 0 {
		timeout = opts.Timeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Validated -c settings precede the subcommand on the argv.
	argv := make([]string, 0, 2*len(opts.Config)+len(opts.Args)+1)
	for _, entry := range opts.Config {
		argv = append(argv, "-c", entry)
	}
	argv = append(argv, opts.Args...)
	cmd := exec.CommandContext(runCtx, e.bin, argv...)
	cmd.Dir = opts.Dir
	cmd.Env = env
	cmd.Stdin = opts.Stdin
	// Return promptly even if a spawned helper holds the pipes open.
	cmd.WaitDelay = 5 * time.Second

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	if opts.Stdout != nil {
		cmd.Stdout = opts.Stdout
	}
	cmd.Stderr = &stderrBuf
	if opts.Stderr != nil {
		cmd.Stderr = io.MultiWriter(opts.Stderr, &stderrBuf)
	}

	runErr := cmd.Run()
	res := &Result{Stdout: stdoutBuf.Bytes(), Stderr: stderrBuf.Bytes()}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return res, &ExitError{Subcommand: sub, Code: exitErr.ExitCode(), Stderr: res.Stderr}
		}
		if runCtx.Err() != nil {
			return res, fmt.Errorf("git %s: %w (context: %v)", sub, runErr, runCtx.Err())
		}
		return res, fmt.Errorf("git %s: %w", sub, runErr)
	}
	return res, nil
}

// inheritedEnv is the allowlist of variables copied from the server process
// into git subprocesses. Everything else is dropped.
var inheritedEnv = []string{
	"PATH", "Path",
	"SystemRoot", // required by Windows process internals
	"TEMP", "TMP",
	"PATHEXT",
	"COMSPEC",
	"USERPROFILE", // Git-for-Windows probes it for its global config location
	"HOME",        // POSIX global config location
}

func buildEnv(extra []string) ([]string, error) {
	if err := validateExtraEnv(extra); err != nil {
		return nil, err
	}
	env := make([]string, 0, len(inheritedEnv)+len(extra)+4)
	for _, key := range inheritedEnv {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	// Neutralize host configuration (threat T8): neither system nor global
	// config may influence server-side git behavior. Note: use "/dev/null"
	// rather than os.DevNull — Git for Windows accepts "/dev/null" (its
	// MSYS layer maps it) but fails on "NUL" with "unable to access".
	env = append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"LC_ALL=C",
		"LANG=C",
	)
	return append(env, extra...), nil
}

func validateExtraEnv(extra []string) error {
	for _, entry := range extra {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" || value == "" {
			return fmt.Errorf("git: invalid environment entry %q", entry)
		}
		if !allowedExtraEnv[key] {
			return fmt.Errorf("git: environment key %q is not allowed", key)
		}
	}
	for _, entry := range extra {
		_, value, _ := strings.Cut(entry, "=")
		if !gitProtocolValue.MatchString(value) {
			return fmt.Errorf("git: invalid GIT_PROTOCOL value %q", value)
		}
	}
	return nil
}

// AtLeast reports whether a "git --version" version string is at least
// major.minor. Non-numeric suffixes (e.g. ".windows.1") are ignored.
func AtLeast(ver string, major, minor int) bool {
	parts := strings.Split(ver, ".")
	if len(parts) < 2 {
		return false
	}
	maj, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	min, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return maj > major || (maj == major && min >= minor)
}
