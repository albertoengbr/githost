// Package app wires configuration, database and HTTP transport into a
// runnable GitHost instance.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"githost.dev/githost/internal/api"
	"githost.dev/githost/internal/config"
	"githost.dev/githost/internal/csrf"
	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/events"
	"githost.dev/githost/internal/git"
	"githost.dev/githost/internal/httpserver"
	"githost.dev/githost/internal/jobs"
	"githost.dev/githost/internal/lock"
	"githost.dev/githost/internal/permissions"
	"githost.dev/githost/internal/prservice"
	"githost.dev/githost/internal/ratelimit"
	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/security"
	"githost.dev/githost/internal/session"
	"githost.dev/githost/internal/storage"
	"githost.dev/githost/internal/store"
	"githost.dev/githost/internal/transport/httpgit"
	"githost.dev/githost/internal/transport/sshgit"
	"githost.dev/githost/internal/web"
	"githost.dev/githost/internal/webhooks"
	"githost.dev/githost/migrations"
)

// Init prepares a GitHost instance: data directory, repositories root,
// instance secret and database schema. It is idempotent: re-running init
// on a healthy instance performs no destructive action.
func Init(ctx context.Context, cfg *config.Config) error {
	if err := os.MkdirAll(cfg.Paths.Data, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if _, err := os.Stat(cfg.Security.SecretFile); errors.Is(err, os.ErrNotExist) {
		if err := security.WriteSecretFile(cfg.Security.SecretFile); err != nil {
			return fmt.Errorf("generate instance secret: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("check secret file: %w", err)
	}

	db, err := database.Open(ctx, cfg.Database.SQLite.Path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := database.Migrate(ctx, db, migrations.FS); err != nil {
		return err
	}
	return nil
}

// RepositoriesRoot returns the on-disk root for bare repositories.
func RepositoriesRoot(cfg *config.Config) string {
	return filepath.Join(cfg.Paths.Data, "repositories")
}

// InstanceSecret loads the instance secret, creating it on first use
// (start before init is an operator mistake we tolerate once, not a
// state we want the server to run in forever).
func InstanceSecret(cfg *config.Config) (string, error) {
	secret, err := security.LoadSecretFile(cfg.Security.SecretFile)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := security.WriteSecretFile(cfg.Security.SecretFile); err != nil {
		return "", err
	}
	return security.LoadSecretFile(cfg.Security.SecretFile)
}

// Server is a running GitHost instance: HTTP listener, optional SSH
// listener, one open database and the exclusive instance lock.
type Server struct {
	cfg    *config.Config
	logger *slog.Logger
	db     *sql.DB
	http   *http.Server
	ssh    *sshgit.Server
	lock   *lock.Lock
	addr   net.Addr
}

// Start opens the database, applies pending migrations (idempotent),
// wires the service graph (store, git executor, repository storage,
// authorization) and starts the HTTP listener with the Git Smart HTTP
// transport mounted. It returns once the server is accepting connections.
// The caller stops it with Stop.
//
// Start holds the exclusive instance lock (internal/lock) for the
// server's lifetime: a second `serve` fails fast, and backup/restore
// refuse to touch a running instance.
func Start(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	instanceLock, err := lock.Acquire(filepath.Join(cfg.Paths.Data, "githost.lock"))
	if err != nil {
		return nil, err
	}
	releaseLock := func() { _ = instanceLock.Release() }

	db, err := database.Open(ctx, cfg.Database.SQLite.Path)
	if err != nil {
		releaseLock()
		return nil, err
	}
	ran, err := database.Migrate(ctx, db, migrations.FS)
	if err != nil {
		db.Close()
		releaseLock()
		return nil, err
	}
	if len(ran) > 0 {
		logger.Info("migrations_applied", "count", len(ran), "versions", fmt.Sprintf("%v", ran))
	}

	// F1 service graph.
	metadata := store.New(db)
	executor, err := git.New(logger)
	if err != nil {
		db.Close()
		releaseLock()
		return nil, err
	}
	repoStorage, err := storage.NewRepositories(RepositoriesRoot(cfg), executor)
	if err != nil {
		db.Close()
		releaseLock()
		return nil, err
	}
	repos := reposervice.New(metadata, repoStorage)
	perms := permissions.New(metadata)
	pulls := prservice.New(metadata, repos, executor)

	// F5: event bus + webhooks + scheduled jobs.
	eventBus := events.New(logger)
	webhookDeliverer := webhooks.New(metadata, logger)
	webhookDeliverer.SubscribeHooks(eventBus)
	jobRunner := jobs.New(metadata, logger)
	jobRunner.Start(ctx)

	// F2: web interface prerequisites. The instance secret signs CSRF
	// tokens; login throttling is per-process (single-instance design).
	secret, err := InstanceSecret(cfg)
	if err != nil {
		db.Close()
		releaseLock()
		return nil, err
	}
	sessions := session.New(metadata, 0)
	csrfManager := csrf.New(secret)
	loginLimiter := ratelimit.New(5, 15*time.Minute)

	var registerErr error
	handler := httpserver.New(httpserver.Options{
		DB:     db,
		Logger: logger,
		RegisterRoutes: func(mux *http.ServeMux) {
			if _, err := web.Register(mux, web.Deps{
				Store:    metadata,
				Sessions: sessions,
				CSRF:     csrfManager,
				Perms:    perms,
				Repos:    repos,
				Git:      executor,
				Limiter:  loginLimiter,
				Logger:   logger,
				Pulls:    pulls,
				Events:   eventBus,
			}); err != nil {
				registerErr = err
				return
			}
			api.Register(mux, api.Deps{
				Store:  metadata,
				Perms:  perms,
				Repos:  repos,
				Git:    executor,
				Logger: logger,
			})
			httpgit.Register(mux, httpgit.Deps{
				Store:        metadata,
				Perms:        perms,
				Repos:        repos,
				Git:          executor,
				Logger:       logger,
				MaxPushBytes: cfg.Limits.MaxPushBytes,
				Events:       eventBus,
			})
		},
	})
	if registerErr != nil {
		db.Close()
		releaseLock()
		return nil, registerErr
	}
	addr := net.JoinHostPort(cfg.Server.Address, strconv.Itoa(cfg.Server.HTTPPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		db.Close()
		releaseLock()
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	httpSrv := &http.Server{
		Handler:           httpgit.Guard(handler.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if cfg.Server.TLS.Enabled {
		// Fail fast on unreadable certificates: a broken TLS config must
		// surface at startup, not on the first request.
		if _, err := os.Stat(cfg.Server.TLS.CertFile); err != nil {
			db.Close()
			ln.Close()
			releaseLock()
			return nil, fmt.Errorf("server.tls.cert_file: %w", err)
		}
		if _, err := os.Stat(cfg.Server.TLS.KeyFile); err != nil {
			db.Close()
			ln.Close()
			releaseLock()
			return nil, fmt.Errorf("server.tls.key_file: %w", err)
		}
	}
	s := &Server{
		cfg:    cfg,
		logger: logger,
		db:     db,
		http:   httpSrv,
		lock:   instanceLock,
		addr:   ln.Addr(),
	}

	// Restricted Git-over-SSH transport (opt-in via configuration).
	if cfg.Server.SSH.Enabled {
		sshServer, err := sshgit.New(sshgit.Deps{
			Store:        metadata,
			Perms:        perms,
			Repos:        repos,
			Git:          executor,
			Logger:       logger,
			HostKeysDir:  filepath.Join(cfg.Paths.Data, "ssh"),
			MaxPushBytes: cfg.Limits.MaxPushBytes,
		})
		if err != nil {
			db.Close()
			ln.Close()
			releaseLock()
			return nil, err
		}
		sshAddr := net.JoinHostPort(cfg.Server.SSH.Address, strconv.Itoa(cfg.Server.SSH.Port))
		bound, err := sshServer.ListenAndServe(sshAddr)
		if err != nil {
			db.Close()
			ln.Close()
			releaseLock()
			return nil, fmt.Errorf("ssh listen on %s: %w", sshAddr, err)
		}
		s.ssh = sshServer
		logger.Info("ssh_server_started", "addr", bound.String())
	}
	go func() {
		if cfg.Server.TLS.Enabled {
			if err := httpSrv.ServeTLS(ln, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("https_server_failed", "err", err)
			}
		} else if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http_server_failed", "err", err)
		}
	}()
	scheme := "http"
	if cfg.Server.TLS.Enabled {
		scheme = "https"
	}
	logger.Info("http_server_started", "addr", ln.Addr().String(), "scheme", scheme)
	return s, nil
}

// Addr returns the bound HTTP listener address (useful when the configured port
// is 0 and the kernel assigned an ephemeral one).
func (s *Server) Addr() net.Addr {
	return s.addr
}

// SSHAddr returns the bound SSH listener address, or nil when the SSH
// transport is disabled.
func (s *Server) SSHAddr() net.Addr {
	if s.ssh == nil {
		return nil
	}
	return s.ssh.Addr()
}

// Config returns the configuration the server was started with.
func (s *Server) Config() *config.Config {
	return s.cfg
}

// Stop shuts down the HTTP server gracefully, stops accepting SSH
// connections, closes the database and releases the instance lock.
func (s *Server) Stop(ctx context.Context) error {
	var errs []error
	if err := s.http.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("http shutdown: %w", err))
	}
	if s.ssh != nil {
		if err := s.ssh.Close(); err != nil {
			errs = append(errs, fmt.Errorf("ssh close: %w", err))
		}
	}
	if err := s.db.Close(); err != nil {
		errs = append(errs, fmt.Errorf("database close: %w", err))
	}
	if s.lock != nil {
		if err := s.lock.Release(); err != nil {
			errs = append(errs, fmt.Errorf("instance lock release: %w", err))
		}
	}
	return errors.Join(errs...)
}
