// Package httpserver exposes GitHost's HTTP transport. F0 provides the
// operational endpoints (/healthz, /version); Git Smart HTTP routes arrive
// in F1 (see docs/research/git-protocols.md).
package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"githost.dev/githost/internal/buildinfo"
)

// Options wires the server's collaborators.
type Options struct {
	// DB is the metadata database; health checks ping it. May be nil in
	// tests that only exercise /version.
	DB     *sql.DB
	Logger *slog.Logger
	// RegisterRoutes, when set, installs additional routes (used by the
	// app layer to mount the Git Smart HTTP transport).
	RegisterRoutes func(mux *http.ServeMux)
}

// Server serves the GitHost HTTP surface.
type Server struct {
	db     *sql.DB
	logger *slog.Logger
	mux    *http.ServeMux
}

// New builds the server and registers the routes.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{
		db:     opts.DB,
		logger: opts.Logger,
		mux:    http.NewServeMux(),
	}
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /version", s.handleVersion)
	if opts.RegisterRoutes != nil {
		opts.RegisterRoutes(s.mux)
	}
	return s
}

// Handler returns the fully middleware-wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	return recoverMiddleware(s.logger, requestLog(s.logger, s.mux))
}

// handleHealthz reports liveness and database reachability. It is designed
// for service managers and monitoring probes: 200 when the instance can
// serve, 503 when the database is unreachable.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	status := http.StatusOK
	dbStatus := "ok"
	if s.db == nil || s.db.PingContext(ctx) != nil {
		status = http.StatusServiceUnavailable
		dbStatus = "error"
	}
	body := map[string]string{
		"status":   "ok",
		"database": dbStatus,
		"version":  buildinfo.Version,
	}
	if dbStatus != "ok" {
		body["status"] = "unavailable"
	}
	writeJSON(w, status, body)
}

// handleVersion reports build metadata.
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":    buildinfo.Version,
		"commit":     buildinfo.Commit,
		"build_date": buildinfo.Date,
		"go":         runtime.Version(),
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
