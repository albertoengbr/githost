// Package httpgit serves the Git Smart HTTP protocol.
//
// GitHost does not reimplement pack negotiation: every request is served
// by the official git binary through internal/git (upload-pack for fetch,
// receive-pack for push) in stateless-RPC mode, exactly as documented in
// docs/research/git-protocols.md. Authorization happens before any git
// process is spawned (threat T3), credentials are access tokens only, and
// the Git-Protocol request header is bridged into the GIT_PROTOCOL
// environment variable (allowlisted) so protocol v2 clients work.
package httpgit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"githost.dev/githost/internal/auth"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/store"
)

// Deps wires the transport to the service graph.
type Deps struct {
	Store  *store.Store
	Perms  *permissions.Service
	Repos  *reposervice.Service
	Git    *git.Executor
	Logger *slog.Logger
	// MaxPushBytes caps receive-pack input (limits.max_push_bytes).
	MaxPushBytes int64
}

// serviceSpec describes one Smart HTTP service.
type serviceSpec struct {
	gitSub  string            // git subcommand: upload-pack | receive-pack
	need    permissions.Level // minimum access level
	scope   string            // required token scope
	push    bool              // writes to the repository
	auditOn string            // audit action for successful transfers
}

var services = map[string]serviceSpec{
	"git-upload-pack": {
		gitSub:  "upload-pack",
		need:    permissions.Read,
		scope:   auth.ScopeRepoRead,
		push:    false,
		auditOn: "git.fetch",
	},
	"git-receive-pack": {
		gitSub:  "receive-pack",
		need:    permissions.Write,
		scope:   auth.ScopeRepoWrite,
		push:    true,
		auditOn: "git.push",
	},
}

type handler struct {
	deps            Deps
	advertiseOnce   sync.Once
	advertiseOption string
}

// Guard wraps an HTTP handler with path-segment validation for the Git
// surface: any request whose (decoded) path contains "." or ".."
// segments is rejected with 400 before the ServeMux can normalize and
// redirect it. Repository names and usernames never contain such
// segments, so legitimate traffic is unaffected.
func Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, segment := range strings.Split(r.URL.Path, "/") {
			if segment == "." || segment == ".." {
				http.Error(w, "invalid path", http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Register installs the Smart HTTP routes on mux.
func Register(mux *http.ServeMux, deps Deps) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	h := &handler{deps: deps}
	mux.HandleFunc("GET /{owner}/{repo}/info/refs", h.handleInfoRefs)
	mux.HandleFunc("POST /{owner}/{repo}/git-upload-pack", h.handleService)
	mux.HandleFunc("POST /{owner}/{repo}/git-receive-pack", h.handleService)
}

// principal is an authenticated caller. Git over HTTPS authenticates with
// access tokens only (spec §8); the username must match the token owner.
type principal struct {
	user  *store.User
	token *store.AccessToken
}

// principalOf extracts and validates credentials. Returns (p, ok):
// p == nil with ok == true means anonymous; ok == false means invalid
// credentials were presented.
func (h *handler) principalOf(r *http.Request) (*principal, bool) {
	username, password, has := r.BasicAuth()
	if !has || username == "" || password == "" {
		return nil, true // anonymous
	}
	token, err := h.deps.Store.TokenByHash(r.Context(), store.HashToken(password))
	if err != nil || !token.Active() {
		return nil, false
	}
	user, err := h.deps.Store.UserByID(r.Context(), token.UserID)
	if err != nil || user.Username != store.NormalizeUsername(username) {
		return nil, false
	}
	return &principal{user: user, token: token}, true
}

// access is a resolved, authorized repository request.
type access struct {
	repo  *store.Repository
	user  *store.User // nil for anonymous
	level permissions.Level
}

// authorize resolves owner/name, checks permissions and writes the
// protocol-mandated status codes. It returns ok == false after writing
// the response.
//
// Status policy (existence must not leak):
//   - anonymous on anything it cannot see → 401 challenge (clients may
//     come back with credentials);
//   - authenticated user with no access → 404 (indistinguishable from a
//     missing repository);
//   - authenticated user with insufficient level (e.g. a read-only member
//     pushing) → 403.
func (h *handler) authorize(w http.ResponseWriter, r *http.Request, p *principal, need permissions.Level) (*access, bool) {
	owner := store.NormalizeUsername(r.PathValue("owner"))
	name := strings.TrimSuffix(r.PathValue("repo"), ".git")

	repo, err := h.deps.Repos.Resolve(r.Context(), owner, name)
	if errors.Is(err, reposervice.ErrNotFound) {
		if p == nil {
			challenge(w)
			return nil, false
		}
		h.deny(r, p, repo, "repository not found")
		http.Error(w, "not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}

	var user *store.User
	if p != nil {
		user = p.user
	}
	level, err := h.deps.Perms.ForRepository(r.Context(), user, repo)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	if !level.AtLeast(need) {
		if user == nil {
			challenge(w)
			return nil, false
		}
		if level == permissions.None {
			// Stranger on a private repository: same answer as missing.
			h.deny(r, p, repo, "no access")
			http.Error(w, "not found", http.StatusNotFound)
			return nil, false
		}
		h.deny(r, p, repo, "insufficient permission "+level.String())
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, false
	}
	return &access{repo: repo, user: user, level: level}, true
}

func (h *handler) deny(r *http.Request, p *principal, repo *store.Repository, reason string) {
	var actor *int64
	if p != nil {
		actor = &p.user.ID
	}
	target := r.PathValue("owner") + "/" + strings.TrimSuffix(r.PathValue("repo"), ".git")
	if err := h.deps.Store.Audit(r.Context(), actor, "git.denied", target, reason); err != nil {
		h.deps.Logger.Error("audit_failed", "err", err)
	}
	h.deps.Logger.Info("git_access_denied",
		"owner", r.PathValue("owner"),
		"reason", reason,
	)
}

func challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="GitHost", charset="UTF-8"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
}

// handleInfoRefs serves GET /{owner}/{repo}/info/refs?service=... — the
// ref advertisement that opens every fetch and push exchange.
func (h *handler) handleInfoRefs(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	spec, ok := services[service]
	if !ok {
		// Protocol: unknown or disabled service → 403.
		http.Error(w, "service not enabled", http.StatusForbidden)
		return
	}
	p, valid := h.principalOf(r)
	if !valid {
		challenge(w)
		return
	}
	if p != nil && !auth.HasScope(p.token.Scopes, spec.scope) {
		http.Error(w, "token lacks the required scope", http.StatusForbidden)
		return
	}
	acc, ok := h.authorize(w, r, p, spec.need)
	if !ok {
		return
	}
	if spec.push && acc.repo.IsArchived {
		http.Error(w, "repository is archived", http.StatusForbidden)
		return
	}
	diskPath, err := h.deps.Repos.DiskPath(acc.repo)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	args := []string{spec.gitSub, "--stateless-rpc", h.advertiseFlag(r.Context()), diskPath}
	res, err := h.deps.Git.Run(r.Context(), git.Options{
		Args:   args,
		Env:    gitProtocolEnv(r),
		Stdout: nil, // advertisement is small; buffer it for clean error handling
	})
	if err != nil {
		h.deps.Logger.Error("git_advertise_failed", "err", err, "stderr", string(res.Stderr))
		http.Error(w, "git service failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "Fri, 01 Jan 1980 00:00:00 GMT")
	w.WriteHeader(http.StatusOK)

	// Protocol v0 requires the "# service=..." pkt-line (plus flush)
	// before git's own advertisement — this is the wrapper that
	// git-http-backend normally writes. Protocol v2 responses start
	// directly with the capability advertisement and must not carry it.
	if !protocolV2(r) {
		_, _ = w.Write(pktLine("# service=" + service + "\n"))
		_, _ = w.Write([]byte("0000"))
	}
	_, _ = w.Write(res.Stdout)
}

// protocolV2 reports whether the client requested protocol version 2.
func protocolV2(r *http.Request) bool {
	for _, value := range strings.Fields(r.Header.Get("Git-Protocol")) {
		if value == "version=2" {
			return true
		}
	}
	return false
}

// pktLine frames one pkt-line (v0 framing).
func pktLine(s string) []byte {
	return []byte(fmt.Sprintf("%04x%s", len(s)+4, s))
}

// handleService serves POST /{owner}/{repo}/git-(upload|receive)-pack —
// the pack transfer itself, streamed in both directions.
func (h *handler) handleService(w http.ResponseWriter, r *http.Request) {
	service := path.Base(r.URL.Path)
	spec, ok := services[service]
	if !ok {
		http.Error(w, "unknown service", http.StatusNotFound)
		return
	}
	p, valid := h.principalOf(r)
	if !valid {
		challenge(w)
		return
	}
	if p != nil && !auth.HasScope(p.token.Scopes, spec.scope) {
		http.Error(w, "token lacks the required scope", http.StatusForbidden)
		return
	}
	acc, ok := h.authorize(w, r, p, spec.need)
	if !ok {
		return
	}
	if spec.push && acc.repo.IsArchived {
		http.Error(w, "repository is archived", http.StatusForbidden)
		return
	}
	diskPath, err := h.deps.Repos.DiskPath(acc.repo)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/x-"+service+"-result")
	w.Header().Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "Fri, 01 Jan 1980 00:00:00 GMT")

	counter := &countingWriter{ResponseWriter: w}
	pushLimit := h.deps.MaxPushBytes
	if pushLimit <= 0 {
		pushLimit = 512 << 20
	}
	runOpts := git.Options{
		Args:   []string{spec.gitSub, "--stateless-rpc", diskPath},
		Env:    gitProtocolEnv(r),
		Stdin:  r.Body,
		Stdout: counter,
	}
	if spec.push {
		// Belt and suspenders: git enforces the pack limit itself
		// (receive.maxInputSize) and the HTTP layer caps the body.
		runOpts.Config = []string{"receive.maxInputSize=" + strconv.FormatInt(pushLimit, 10)}
		if pushLimit > 0 {
			runOpts.Stdin = http.MaxBytesReader(w, r.Body, pushLimit)
		}
	}
	_, err = h.deps.Git.Run(r.Context(), runOpts)
	if err != nil {
		var exitErr *git.ExitError
		if errors.As(err, &exitErr) && counter.written == 0 {
			http.Error(w, "git service failed", http.StatusInternalServerError)
		}
		// Otherwise bytes already went to the client (pack stream with
		// its own error semantics); only log server-side.
		h.deps.Logger.Error("git_service_failed",
			"service", service,
			"err", err,
			"bytes_written", counter.written,
		)
		return
	}

	var actor *int64
	if p != nil {
		actor = &p.user.ID
	}
	if err := h.deps.Store.Audit(r.Context(), actor, spec.auditOn,
		acc.repo.Owner+"/"+acc.repo.Name, "via https"); err != nil {
		h.deps.Logger.Error("audit_failed", "err", err)
	}
}

// gitProtocolEnv bridges the Git-Protocol request header into the
// GIT_PROTOCOL environment variable (protocol v2 support). The executor
// re-validates the value before it reaches a process.
func gitProtocolEnv(r *http.Request) []string {
	for _, value := range strings.Fields(r.Header.Get("Git-Protocol")) {
		if strings.HasPrefix(value, "version=") {
			return []string{"GIT_PROTOCOL=" + value}
		}
	}
	return nil
}

// advertiseFlag picks the ref-advertisement flag name for the installed
// git: --http-backend-info-refs from 2.34 on, --advertise-refs before.
func (h *handler) advertiseFlag(ctx context.Context) string {
	h.advertiseOnce.Do(func() {
		h.advertiseOption = "--advertise-refs"
		if ver, err := h.deps.Git.Version(ctx); err == nil && git.AtLeast(ver, 2, 34) {
			h.advertiseOption = "--http-backend-info-refs"
		}
	})
	return h.advertiseOption
}

// countingWriter tracks how many bytes reached the client, to decide
// whether an error response can still be written.
type countingWriter struct {
	http.ResponseWriter
	written int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	return n, err
}
