package httpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"githost.dev/githost/internal/database"
	"githost.dev/githost/migrations"
)

// newTestServer returns a Server backed by a real migrated SQLite database.
func newTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	db, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if _, err := database.Migrate(context.Background(), db, migrations.FS); err != nil {
		db.Close()
		t.Fatalf("migrate test db: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(Options{DB: db, Logger: logger}), func() { db.Close() }
}

func TestHealthzOK(t *testing.T) {
	s, cleanup := newTestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Status   string `json:"status"`
		Database string `json:"database"`
		Version  string `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	if body.Status != "ok" || body.Database != "ok" {
		t.Fatalf("body = %+v", body)
	}
	if body.Version == "" {
		t.Fatal("version missing from healthz")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestHealthzReportsDatabaseFailure(t *testing.T) {
	s, cleanup := newTestServer(t)
	cleanup() // close the database to force the failure path

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "unavailable" || body.Database != "error" {
		t.Fatalf("body = %+v", body)
	}
}

func TestVersionEndpoint(t *testing.T) {
	s, cleanup := newTestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Version string `json:"version"`
		OS      string `json:"os"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Version == "" || body.OS == "" {
		t.Fatalf("body = %+v", body)
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	s, cleanup := newTestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no-such-route", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHealthzRejectsNonGET(t *testing.T) {
	s, cleanup := newTestServer(t)
	defer cleanup()

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestRecoverMiddleware(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	boom := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})

	rec := httptest.NewRecorder()
	recoverMiddleware(logger, boom).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
