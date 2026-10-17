// Package sshgit implements GitHost's restricted SSH transport.
//
// The server speaks public-key authentication only and accepts exactly
// one command grammar: `git-upload-pack '<repo>'` or
// `git-receive-pack '<repo>'`. Shells, PTYs, port forwarding and every
// other channel type are refused (threat T4 in docs/security/threat-model.md).
// Repository paths resolve through the metadata store to UUID-addressed
// directories before any git process starts (T2/T3).
package sshgit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/store"
)

// Deps wires the SSH transport to the service graph.
type Deps struct {
	Store       *store.Store
	Perms       *permissions.Service
	Repos       *reposervice.Service
	Git         *git.Executor
	Logger      *slog.Logger
	HostKeysDir string
	// MaxPushBytes caps receive-pack input (limits.max_push_bytes).
	MaxPushBytes int64
}

// Server is the restricted Git-over-SSH server.
type Server struct {
	deps     Deps
	hostKeys []ssh.Signer
	listener net.Listener
	logger   *slog.Logger
}

// New builds the server, loading or generating the host key.
func New(deps Deps) (*Server, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	signer, err := loadOrGenerateHostKey(deps.HostKeysDir)
	if err != nil {
		return nil, fmt.Errorf("ssh host key: %w", err)
	}
	return &Server{deps: deps, hostKeys: []ssh.Signer{signer}, logger: deps.Logger}, nil
}

// ListenAndServe binds addr (port 0 picks an ephemeral port, returned to
// the caller) and serves connections until Close is called. Running
// sessions are not interrupted by Close; they finish with the git client.
func (s *Server) ListenAndServe(addr string) (net.Addr, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.listener = ln
	go s.acceptLoop(ln)
	return ln.Addr(), nil
}

// Close stops accepting new connections.
func (s *Server) Close() error {
	if s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

// Addr returns the bound listener address.
func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

func (s *Server) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.logger.Error("ssh_accept_failed", "err", err)
			}
			return
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	ctx := context.Background()
	defer conn.Close()

	config := &ssh.ServerConfig{
		MaxAuthTries: 3,
		// Public-key authentication only: no password callback means
		// password auth is impossible.
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			return s.authenticate(ctx, meta, key)
		},
	}
	for _, hostKey := range s.hostKeys {
		config.AddHostKey(hostKey)
	}

	sconn, channels, globalRequests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		// Includes failed handshakes and exhausted auth attempts.
		s.logger.Info("ssh_auth_failed", "remote", conn.RemoteAddr().String(), "err", err.Error())
		return
	}
	defer sconn.Close()

	s.audit(ctx, sconn, "auth.ssh.login", "", "")

	// Global requests (tcpip-forward and friends) are all refused by
	// simply discarding them.
	go ssh.DiscardRequests(globalRequests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			newChannel.Reject(ssh.Prohibited, "only session channels are allowed")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			s.logger.Error("ssh_channel_accept_failed", "err", err)
			continue
		}
		go s.handleSession(ctx, sconn, channel, requests)
	}
}

// authenticate maps a presented public key to a GitHost user.
func (s *Server) authenticate(ctx context.Context, meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	fingerprint := ssh.FingerprintSHA256(key)
	k, err := s.deps.Store.SSHKeyByFingerprint(ctx, fingerprint)
	if err != nil {
		return nil, fmt.Errorf("unknown public key %s", fingerprint)
	}
	user, err := s.deps.Store.UserByID(ctx, k.UserID)
	if err != nil {
		return nil, fmt.Errorf("key %s has no user", fingerprint)
	}
	return &ssh.Permissions{
		Extensions: map[string]string{
			"user_id":  strconv.FormatInt(user.ID, 10),
			"username": user.Username,
		},
	}, nil
}

// handleSession enforces the exec-only session policy and dispatches the
// single allowed command shape to the git engine.
func (s *Server) handleSession(ctx context.Context, conn *ssh.ServerConn, channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()

	var extraEnv []string
	for req := range requests {
		switch req.Type {
		case "env":
			// Only GIT_PROTOCOL passes through (protocol v2 opt-in);
			// the executor re-validates the value.
			var payload struct {
				Name  string
				Value string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err == nil &&
				payload.Name == "GIT_PROTOCOL" && strings.HasPrefix(payload.Value, "version=") {
				extraEnv = append(extraEnv, payload.Name+"="+payload.Value)
			}
			if req.WantReply {
				req.Reply(true, nil)
			}

		case "pty-req":
			if req.WantReply {
				req.Reply(false, nil)
			}

		case "shell", "subsystem":
			// Interactive sessions are categorically refused.
			fmt.Fprintln(channel.Stderr(), "GitHost: interactive sessions are disabled; only git operations are allowed.")
			if req.WantReply {
				req.Reply(false, nil)
			}
			s.auditDenied(ctx, conn, "", "rejected "+req.Type+" request")
			channel.Close()
			return

		case "exec":
			var payload struct {
				Command string
			}
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				if req.WantReply {
					req.Reply(false, nil)
				}
				return
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
			s.serveExec(ctx, conn, channel, payload.Command, extraEnv)
			return

		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// gitServices maps the two allowed exec commands to their requirements.
var gitServices = map[string]struct {
	gitSub  string
	push    bool
	auditOn string
}{
	"git-upload-pack":  {"upload-pack", false, "git.fetch"},
	"git-receive-pack": {"receive-pack", true, "git.push"},
}

// serveExec runs one authorized git service for the session.
func (s *Server) serveExec(ctx context.Context, conn *ssh.ServerConn, channel ssh.Channel, command string, extraEnv []string) {
	userID, _ := strconv.ParseInt(conn.Permissions.Extensions["user_id"], 10, 64)

	service, repoPath, ok := parseGitCommand(command)
	if !ok {
		fmt.Fprintln(channel.Stderr(), "GitHost: only 'git-upload-pack' and 'git-receive-pack' commands are allowed.")
		s.auditDenied(ctx, conn, "", "invalid command "+strconv.Quote(command))
		s.exit(channel, 1)
		return
	}
	spec := gitServices[service]

	owner, name, ok := splitRepoPath(repoPath)
	if !ok {
		fmt.Fprintln(channel.Stderr(), "GitHost: invalid repository path.")
		s.auditDenied(ctx, conn, repoPath, "invalid repository path")
		s.exit(channel, 1)
		return
	}

	repo, err := s.deps.Repos.Resolve(ctx, owner, name)
	if err != nil {
		// Existence is not leaked: unknown and forbidden look the same.
		fmt.Fprintln(channel.Stderr(), "repository not found")
		s.auditDenied(ctx, conn, owner+"/"+name, "repository not found")
		s.exit(channel, 1)
		return
	}

	user, err := s.deps.Store.UserByID(ctx, userID)
	if err != nil {
		s.exit(channel, 1)
		return
	}
	need := permissions.Read
	if spec.push {
		need = permissions.Write
	}
	level, err := s.deps.Perms.ForRepository(ctx, user, repo)
	if err != nil {
		fmt.Fprintln(channel.Stderr(), "internal error")
		s.exit(channel, 1)
		return
	}
	if !level.AtLeast(need) {
		if level == permissions.None {
			fmt.Fprintln(channel.Stderr(), "repository not found")
		} else {
			fmt.Fprintln(channel.Stderr(), "permission denied")
		}
		s.auditDenied(ctx, conn, owner+"/"+name, "insufficient level "+level.String()+" for "+service)
		s.exit(channel, 1)
		return
	}
	if spec.push && repo.IsArchived {
		fmt.Fprintln(channel.Stderr(), "repository is archived")
		s.auditDenied(ctx, conn, owner+"/"+name, "push to archived repository")
		s.exit(channel, 1)
		return
	}

	diskPath, err := s.deps.Repos.DiskPath(repo)
	if err != nil {
		fmt.Fprintln(channel.Stderr(), "internal error")
		s.exit(channel, 1)
		return
	}

	// SSH is stateful: git drives the whole protocol conversation over
	// the channel; no --stateless-rpc here.
	runOpts := git.Options{
		Args:   []string{spec.gitSub, diskPath},
		Env:    extraEnv,
		Stdin:  channel,
		Stdout: channel,
		Stderr: channel.Stderr(),
	}
	if spec.push {
		limit := s.deps.MaxPushBytes
		if limit <= 0 {
			limit = 512 << 20
		}
		runOpts.Config = []string{"receive.maxInputSize=" + strconv.FormatInt(limit, 10)}
	}
	_, runErr := s.deps.Git.Run(ctx, runOpts)
	if runErr != nil {
		s.logger.Error("ssh_git_service_failed", "service", service, "err", runErr)
	}
	status := 0
	var exitErr *git.ExitError
	if errors.As(runErr, &exitErr) {
		status = exitErr.Code
	}
	s.exit(channel, uint32(status))
	if runErr == nil {
		s.audit(ctx, conn, spec.auditOn, owner+"/"+name, "via ssh")
	}
}

// exit sends the exec exit status and closes the channel.
func (s *Server) exit(channel ssh.Channel, status uint32) {
	payload := ssh.Marshal(struct{ Status uint32 }{Status: status})
	_, _ = channel.SendRequest("exit-status", false, payload)
	_ = channel.Close()
}

// parseGitCommand accepts exactly "git-upload-pack '<path>'" or
// "git-receive-pack '<path>'" (git single-quotes the path).
func parseGitCommand(command string) (service, path string, ok bool) {
	fields := strings.Fields(command)
	if len(fields) != 2 {
		return "", "", false
	}
	if _, allowed := gitServices[fields[0]]; !allowed {
		return "", "", false
	}
	arg := fields[1]
	if len(arg) < 2 || !strings.HasPrefix(arg, "'") || !strings.HasSuffix(arg, "'") {
		return "", "", false
	}
	return fields[0], strings.Trim(arg, "'"), true
}

// splitRepoPath parses "owner/name.git" (leading slash tolerated).
func splitRepoPath(path string) (owner, name string, ok bool) {
	path = strings.TrimPrefix(path, "/")
	owner, name, found := strings.Cut(path, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", false
	}
	name = strings.TrimSuffix(name, ".git")
	if owner != store.NormalizeUsername(owner) || name == "" {
		return "", "", false
	}
	return owner, name, true
}

func (s *Server) audit(ctx context.Context, conn *ssh.ServerConn, action, target, detail string) {
	s.auditAs(ctx, conn, action, target, detail)
}

func (s *Server) auditDenied(ctx context.Context, conn *ssh.ServerConn, target, detail string) {
	s.auditAs(ctx, conn, "git.denied", target, detail)
}

func (s *Server) auditAs(ctx context.Context, conn *ssh.ServerConn, action, target, detail string) {
	userID, _ := strconv.ParseInt(conn.Permissions.Extensions["user_id"], 10, 10)
	if err := s.deps.Store.Audit(ctx, &userID, action, target, detail); err != nil {
		s.logger.Error("audit_failed", "err", err)
	}
}
