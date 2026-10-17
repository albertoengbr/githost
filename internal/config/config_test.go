package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsResolveAndValidate(t *testing.T) {
	cfg := Default()
	if err := cfg.Resolve(t.TempDir()); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Server.Address != DefaultAddress || cfg.Server.HTTPPort != DefaultHTTPPort {
		t.Fatalf("unexpected server defaults: %+v", cfg.Server)
	}
	if cfg.Database.Driver != DriverSQLite {
		t.Fatalf("default driver = %q, want %q", cfg.Database.Driver, DriverSQLite)
	}
	if cfg.Security.AllowRegistration {
		t.Fatal("public registration must be disabled by default")
	}
	if want := filepath.Join(cfg.Paths.Data, DefaultDatabaseFile); cfg.Database.SQLite.Path != want {
		t.Fatalf("sqlite path = %q, want %q", cfg.Database.SQLite.Path, want)
	}
}

func TestLoadAppliesDefaultsAndResolvesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "githost.yaml")
	body := "server:\n  http_port: 3100\npaths:\n  data: mydata\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.HTTPPort != 3100 {
		t.Fatalf("http_port = %d, want 3100", cfg.Server.HTTPPort)
	}
	if cfg.Server.Address != DefaultAddress {
		t.Fatalf("address default not applied: %q", cfg.Server.Address)
	}
	wantData := filepath.Join(dir, "mydata")
	if cfg.Paths.Data != wantData {
		t.Fatalf("data = %q, want %q", cfg.Paths.Data, wantData)
	}
	if want := filepath.Join(wantData, DefaultDatabaseFile); cfg.Database.SQLite.Path != want {
		t.Fatalf("sqlite path = %q, want %q", cfg.Database.SQLite.Path, want)
	}
	if want := filepath.Join(wantData, DefaultSecretFile); cfg.Security.SecretFile != want {
		t.Fatalf("secret file = %q, want %q", cfg.Security.SecretFile, want)
	}
}

func TestLoadKeepsAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	absData := filepath.Join(dir, "elsewhere")
	cfgPath := filepath.Join(dir, "githost.yaml")
	body := "paths:\n  data: " + absData + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Paths.Data != absData {
		t.Fatalf("data = %q, want %q", cfg.Paths.Data, absData)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

func TestLoadMalformedYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "githost.yaml")
	if err := os.WriteFile(path, []byte("server: [oops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("want error for malformed YAML")
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*Config)
		wantField string
	}{
		{"empty address", func(c *Config) { c.Server.Address = "" }, "server.address"},
		{"address with port", func(c *Config) { c.Server.Address = "127.0.0.1:3000" }, "server.address"},
		{"http port zero", func(c *Config) { c.Server.HTTPPort = 0 }, "server.http_port"},
		{"http port too high", func(c *Config) { c.Server.HTTPPort = 70000 }, "server.http_port"},
		{"ssh port negative", func(c *Config) { c.Server.SSH.Port = -1 }, "server.ssh.port"},
		{"bad driver", func(c *Config) { c.Database.Driver = "mysql" }, "database.driver"},
		{"bad level", func(c *Config) { c.Logging.Level = "verbose" }, "logging.level"},
		{"bad format", func(c *Config) { c.Logging.Format = "xml" }, "logging.format"},
		{"relative data", func(c *Config) { c.Paths.Data = "relative" }, "paths.data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			if err := cfg.Resolve(t.TempDir()); err != nil {
				t.Fatal(err)
			}
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("want validation error for %s", tc.name)
			}
			if !strings.HasPrefix(err.Error(), tc.wantField+":") {
				t.Fatalf("err = %q, want prefix %q", err.Error(), tc.wantField+":")
			}
		})
	}
}

func TestEnsureFileCreatesOnceAndGeneratesValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "githost.yaml")

	created, err := EnsureFile(path)
	if err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}
	if !created {
		t.Fatal("first EnsureFile should create the file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file missing: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}

	created2, err := EnsureFile(path)
	if err != nil {
		t.Fatalf("second EnsureFile: %v", err)
	}
	if created2 {
		t.Fatal("second EnsureFile must not recreate the file")
	}
}
