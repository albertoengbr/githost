// Package config implements loading, defaulting, path resolution and
// validation of the GitHost YAML configuration.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Built-in defaults used when the configuration file omits a field.
const (
	DefaultAddress      = "127.0.0.1"
	DefaultHTTPPort     = 3000
	DefaultSSHAddress   = "0.0.0.0"
	DefaultSSHPort      = 2222
	DefaultDataDir      = "data"
	DefaultDatabaseFile = "githost.db"
	DefaultSecretFile   = "secret.key"
	DefaultLogLevel     = "info"
	DefaultLogFormat    = "json"
)

// DriverSQLite is the only metadata driver supported in F0.
const DriverSQLite = "sqlite"

// DefaultConfigPath is the configuration file used by the CLI when
// --config is not given.
const DefaultConfigPath = "githost.yaml"

// Config is the fully resolved runtime configuration. After Load, every
// path field is absolute.
type Config struct {
	Server   Server   `yaml:"server"`
	Paths    Paths    `yaml:"paths"`
	Database Database `yaml:"database"`
	Logging  Logging  `yaml:"logging"`
	Security Security `yaml:"security"`
	Limits   Limits   `yaml:"limits"`
}

// Server holds the HTTP listener settings, the SSH transport settings
// and the native TLS settings.
type Server struct {
	Address  string `yaml:"address"`
	HTTPPort int    `yaml:"http_port"`
	SSH      SSH    `yaml:"ssh"`
	TLS      TLS    `yaml:"tls"`
}

// SSH configures the Git-over-SSH transport (F1).
type SSH struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

// TLS configures native HTTPS serving (F3). When enabled, the HTTP
// listener serves TLS directly; a reverse proxy is no longer required.
type TLS struct {
	Enabled  bool   `yaml:"enabled"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
}

// Paths holds filesystem locations. Relative values resolve against the
// directory containing the configuration file.
type Paths struct {
	Data string `yaml:"data"`
}

// Database selects the metadata store and its settings.
type Database struct {
	Driver string `yaml:"driver"`
	SQLite SQLite `yaml:"sqlite"`
}

// SQLite settings. An empty Path means "<paths.data>/githost.db".
type SQLite struct {
	Path string `yaml:"path"`
}

// Logging configures the structured logger.
type Logging struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Security holds instance-wide security switches. Public registration is
// disabled by default; instance secret material lives in SecretFile.
type Security struct {
	SecretFile        string `yaml:"secret_file"`
	AllowRegistration bool   `yaml:"allow_registration"`
}

// Limits bounds resource usage. MaxPushBytes caps the pack size a
// receive-pack accepts (enforced via git's receive.maxInputSize and the
// HTTP request body limit).
type Limits struct {
	MaxPushBytes int64 `yaml:"max_push_bytes"`
}

// DefaultMaxPushBytes caps pushes at 512 MiB unless configured.
const DefaultMaxPushBytes = 512 << 20

// Default returns the built-in default configuration with relative paths.
func Default() *Config {
	return &Config{
		Server: Server{
			Address:  DefaultAddress,
			HTTPPort: DefaultHTTPPort,
			SSH: SSH{
				Enabled: false,
				Address: DefaultSSHAddress,
				Port:    DefaultSSHPort,
			},
		},
		Paths: Paths{Data: DefaultDataDir},
		Database: Database{
			Driver: DriverSQLite,
		},
		Logging: Logging{
			Level:  DefaultLogLevel,
			Format: DefaultLogFormat,
		},
		Security: Security{
			SecretFile:        "",
			AllowRegistration: false,
		},
		Limits: Limits{
			MaxPushBytes: DefaultMaxPushBytes,
		},
	}
}

// Load reads the YAML configuration at path, overlays it on the defaults,
// resolves relative paths against the config file directory (or the working
// directory when path is empty) and validates the result. A missing or
// malformed file is an error.
func Load(path string) (*Config, error) {
	cfg := Default()
	baseDir := ""
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve config path %s: %w", path, err)
		}
		baseDir = filepath.Dir(abs)
	}
	if err := cfg.Resolve(baseDir); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Resolve turns every path field absolute. Relative paths resolve against
// baseDir; an empty baseDir means the process working directory. Derived
// defaults (SQLite path, secret file) are filled in from the data directory.
func (c *Config) Resolve(baseDir string) error {
	if baseDir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("determine working directory: %w", err)
		}
		baseDir = cwd
	}
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(baseDir, p)
	}
	c.Paths.Data = abs(c.Paths.Data)
	if c.Database.SQLite.Path == "" {
		c.Database.SQLite.Path = filepath.Join(c.Paths.Data, DefaultDatabaseFile)
	} else {
		c.Database.SQLite.Path = abs(c.Database.SQLite.Path)
	}
	if c.Security.SecretFile == "" {
		c.Security.SecretFile = filepath.Join(c.Paths.Data, DefaultSecretFile)
	} else {
		c.Security.SecretFile = abs(c.Security.SecretFile)
	}
	if c.Server.TLS.CertFile != "" {
		c.Server.TLS.CertFile = abs(c.Server.TLS.CertFile)
	}
	if c.Server.TLS.KeyFile != "" {
		c.Server.TLS.KeyFile = abs(c.Server.TLS.KeyFile)
	}
	if c.Limits.MaxPushBytes <= 0 {
		c.Limits.MaxPushBytes = DefaultMaxPushBytes
	}
	return nil
}

// Validate checks a resolved configuration and returns a descriptive error
// for the first violation found.
func (c *Config) Validate() error {
	if err := validateListenAddress("server.address", c.Server.Address); err != nil {
		return err
	}
	if err := validatePort("server.http_port", c.Server.HTTPPort); err != nil {
		return err
	}
	if err := validateListenAddress("server.ssh.address", c.Server.SSH.Address); err != nil {
		return err
	}
	if err := validatePort("server.ssh.port", c.Server.SSH.Port); err != nil {
		return err
	}
	if !filepath.IsAbs(c.Paths.Data) {
		return fmt.Errorf("paths.data: %q is not absolute (call Resolve first)", c.Paths.Data)
	}
	if c.Database.Driver != DriverSQLite {
		return fmt.Errorf("database.driver: %q is not supported (supported: %q)", c.Database.Driver, DriverSQLite)
	}
	if !filepath.IsAbs(c.Database.SQLite.Path) {
		return fmt.Errorf("database.sqlite.path: %q is not absolute (call Resolve first)", c.Database.SQLite.Path)
	}
	switch c.Logging.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("logging.level: %q is not one of debug, info, warn, error", c.Logging.Level)
	}
	switch c.Logging.Format {
	case "json", "text":
	default:
		return fmt.Errorf("logging.format: %q is not one of json, text", c.Logging.Format)
	}
	if !filepath.IsAbs(c.Security.SecretFile) {
		return fmt.Errorf("security.secret_file: %q is not absolute (call Resolve first)", c.Security.SecretFile)
	}
	if c.Limits.MaxPushBytes <= 0 {
		return fmt.Errorf("limits.max_push_bytes: must be a positive number of bytes")
	}
	if c.Server.TLS.Enabled {
		if c.Server.TLS.CertFile == "" || c.Server.TLS.KeyFile == "" {
			return fmt.Errorf("server.tls: cert_file and key_file are required when tls is enabled")
		}
	}
	return nil
}

func validatePort(field string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%s: %d is out of range 1..65535", field, port)
	}
	return nil
}

func validateListenAddress(field, addr string) error {
	if addr == "" {
		return fmt.Errorf("%s: must not be empty", field)
	}
	if strings.ContainsAny(addr, " \t:/") {
		return fmt.Errorf("%s: %q must be a host or IP without scheme or port", field, addr)
	}
	if len(addr) > 253 {
		return fmt.Errorf("%s: host name too long", field)
	}
	return nil
}
