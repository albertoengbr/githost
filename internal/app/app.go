// Package app wires configuration, database and HTTP transport into a
// runnable GitHost instance.
package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"githost.dev/githost/internal/config"
	"githost.dev/githost/internal/database"
	"githost.dev/githost/internal/httpserver"
	"githost.dev/githost/internal/security"
	"githost.dev/githost/migrations"
)

// Init prepares a GitHost instance: data directory, instance secret and
// database schema. It is idempotent: re-running init on a healthy instance
// performs no destructive action.
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

// Server is a running GitHost instance: HTTP listener plus open database.
type Server struct {
	cfg    *config.Config
	logger *slog.Logger
	db     *sql.DB
	http   *http.Server
	addr   net.Addr
}

// Start opens the database, applies pending migrations (idempotent) and
// starts the HTTP listener. It returns once the server is accepting
// connections. The caller stops it with Stop.
func Start(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	db, err := database.Open(ctx, cfg.Database.SQLite.Path)
	if err != nil {
		return nil, err
	}
	ran, err := database.Migrate(ctx, db, migrations.FS)
	if err != nil {
		db.Close()
		return nil, err
	}
	if len(ran) > 0 {
		logger.Info("migrations_applied", "count", len(ran), "versions", fmt.Sprintf("%v", ran))
	}

	handler := httpserver.New(httpserver.Options{DB: db, Logger: logger})
	addr := net.JoinHostPort(cfg.Server.Address, strconv.Itoa(cfg.Server.HTTPPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	httpSrv := &http.Server{
		Handler:           handler.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	s := &Server{
		cfg:    cfg,
		logger: logger,
		db:     db,
		http:   httpSrv,
		addr:   ln.Addr(),
	}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http_server_failed", "err", err)
		}
	}()
	logger.Info("http_server_started", "addr", ln.Addr().String())
	return s, nil
}

// Addr returns the bound listener address (useful when the configured port
// is 0 and the kernel assigned an ephemeral one).
func (s *Server) Addr() net.Addr {
	return s.addr
}

// Config returns the configuration the server was started with.
func (s *Server) Config() *config.Config {
	return s.cfg
}

// Stop shuts down the HTTP server gracefully and closes the database.
func (s *Server) Stop(ctx context.Context) error {
	var errs []error
	if err := s.http.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("http shutdown: %w", err))
	}
	if err := s.db.Close(); err != nil {
		errs = append(errs, fmt.Errorf("database close: %w", err))
	}
	return errors.Join(errs...)
}
