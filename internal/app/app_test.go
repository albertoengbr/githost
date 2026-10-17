package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"githost.dev/githost/internal/config"
)

// testConfig builds a valid configuration rooted at a data directory whose
// path deliberately contains a space (common on Windows) and with an
// ephemeral HTTP port.
func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Default()
	if err := cfg.Resolve(filepath.Join(t.TempDir(), "githost data")); err != nil {
		t.Fatal(err)
	}
	cfg.Server.HTTPPort = 0
	return cfg
}

func TestInitCreatesInstance(t *testing.T) {
	cfg := testConfig(t)
	if err := Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, path := range []string{cfg.Paths.Data, cfg.Database.SQLite.Path, cfg.Security.SecretFile} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing after init: %s (%v)", path, err)
		}
	}
	// Init is idempotent.
	if err := Init(context.Background(), cfg); err != nil {
		t.Fatalf("second Init: %v", err)
	}
}

func TestStartServeStop(t *testing.T) {
	cfg := testConfig(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Init(ctx, cfg); err != nil {
		t.Fatalf("Init: %v", err)
	}
	srv, err := Start(ctx, cfg, logger)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// The reported address must be dialable and answer health checks.
	addr := srv.Addr().String()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d", resp.StatusCode)
	}
	var body struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ok" || body.Database != "ok" {
		t.Fatalf("healthz body = %+v", body)
	}

	stopCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := srv.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop the listener must be closed.
	if _, err := client.Get("http://" + addr + "/healthz"); err == nil {
		t.Fatal("server still serving after Stop")
	}
}

func TestStartRejectsOccupiedPort(t *testing.T) {
	cfg := testConfig(t)
	// Occupy a port first.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg.Server.HTTPPort = ln.Addr().(*net.TCPAddr).Port

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := Start(context.Background(), cfg, logger); err == nil {
		t.Fatal("Start must fail when the port is occupied")
	}
}
